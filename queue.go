package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusConverting Status = "converting"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
)

// ---------- staged files (step 1) ----------

type Staged struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`            // engine label, e.g. Keynote
	Slides bool   `json:"slides"`          // presentations (slide options apply)
	Error  string `json:"error,omitempty"` // why it can't be converted
	path   string // file on disk
	owned  bool   // path lives in our inbox (uploaded copy)
	dir    string // inbox folder for this file
}

// ---------- jobs (steps 2–3) ----------

type Job struct {
	ID       string
	BatchID  string
	Name     string // original file name
	Plan     plan
	Opts     Options
	Src      string
	WorkDir  string
	OwnsSrc  bool
	OutDir   string // overrides converted/<id> (CLI)
	Combined bool   // the "combine into one PDF" result
	Status   Status
	Phase    string // what's happening right now, for the UI
	Err      string
	OutPath  string
	Size     int64
	Pages    int
	SavedTo  string
	SentTo   string // "cloud" or "usb" once it's on the reMarkable
	Sending  bool
	SendErr  string
	Created  time.Time
	Started  time.Time
	Finished time.Time
	done     chan struct{}
}

type JobView struct {
	ID        string `json:"id"`
	Batch     string `json:"batch"`
	Name      string `json:"name"`
	OutName   string `json:"outName"`
	Kind      string `json:"kind"`
	Status    Status `json:"status"`
	Phase     string `json:"phase,omitempty"`
	Error     string `json:"error,omitempty"`
	CanRetry  bool   `json:"canRetry,omitempty"`
	Combined  bool   `json:"combined,omitempty"`
	Size      int64  `json:"size"`
	Pages     int    `json:"pages"`
	SavedTo   string `json:"savedTo,omitempty"`
	SentTo    string `json:"sentTo,omitempty"`
	Sending   bool   `json:"sending,omitempty"`
	SendError string `json:"sendError,omitempty"`
	Created   int64  `json:"created"`
	ElapsedMs int64  `json:"elapsedMs"`
}

type Batch struct {
	ID      string
	JobIDs  []string
	Opts    Options
	Created time.Time
	merged  bool
	adding  bool   // jobs still being submitted
	SendTo  string // "cloud"/"usb": send each PDF to the reMarkable when it's ready
}

type Queue struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	batches  map[string]*Batch
	staged   []*Staged
	chans    map[Kind]chan *Job
	conv     Converter
	paths    Paths
	avail    Avail
	settings *SettingsStore
	ctx      context.Context
	libPath  string
	persist  bool
	idleWait time.Duration
	post     func(in, out string, o Options) error
	merge    func(files []string, out string) error
	rm       *RMClient
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func NewQueue(ctx context.Context, p Paths, a Avail, conv Converter, st *SettingsStore, persist bool) *Queue {
	q := &Queue{
		jobs: map[string]*Job{}, batches: map[string]*Batch{}, chans: map[Kind]chan *Job{},
		conv: conv, paths: p, avail: a, settings: st, ctx: ctx,
		libPath: filepath.Join(p.State, "library.json"), persist: persist,
		idleWait: 20 * time.Second, post: postProcess, merge: mergePDFs,
	}
	// Each Apple app handles one document at a time; images & PDFs run in parallel.
	workers := map[Kind]int{
		KindKeynote: 1, KindPages: 1, KindNumbers: 1, KindLibreOffice: 1,
		KindImage: max(2, runtime.NumCPU()), KindPDF: max(2, runtime.NumCPU()/2),
	}
	for k, n := range workers {
		ch := make(chan *Job, 4096)
		q.chans[k] = ch
		for i := 0; i < n; i++ {
			go q.worker(k, ch, i == 0 && k.bundleID() != "")
		}
	}
	if persist {
		q.loadLibrary()
	}
	return q
}

func (q *Queue) worker(k Kind, ch chan *Job, tracksApp bool) {
	idle := time.NewTimer(time.Hour)
	idle.Stop()
	for {
		select {
		case <-q.ctx.Done():
			return
		case j := <-ch:
			idle.Stop()
			q.run(j)
			if tracksApp && len(ch) == 0 {
				idle.Reset(q.idleWait)
			}
		case <-idle.C:
			q.conv.AppIdle(k)
		}
	}
}

// ---------- staging ----------

func (q *Queue) newStaged(name, path string, owned bool, dir string) *Staged {
	s := &Staged{ID: newID(), Name: name, path: path, owned: owned, dir: dir}
	if fi, err := os.Stat(path); err == nil {
		s.Size = fi.Size()
	}
	if p, err := planFor(name, q.avail); err != nil {
		s.Error = err.Error()
	} else {
		s.Kind = p.Kind.Label()
		s.Slides = isSlideExt(name)
	}
	return s
}

func isSlideExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pptx", ".ppt", ".pptm", ".ppsx", ".pps", ".potx", ".key", ".odp":
		return true
	}
	return false
}

// NewInboxDir reserves a scratch folder for an incoming upload.
func (q *Queue) NewInboxDir() (string, error) {
	dir := filepath.Join(q.paths.Inbox, newID())
	return dir, os.MkdirAll(dir, 0o755)
}

// StageUpload registers a file that was uploaded into dir.
func (q *Queue) StageUpload(dir, path, name string) Staged {
	s := q.newStaged(name, path, true, dir)
	q.mu.Lock()
	q.staged = append(q.staged, s)
	q.mu.Unlock()
	return *s
}

// StagePaths adds files (and the supported files inside folders) by path.
func (q *Queue) StagePaths(paths []string) int {
	var add []*Staged
	seen := map[string]bool{}
	q.mu.Lock()
	for _, s := range q.staged {
		seen[s.path] = true
	}
	q.mu.Unlock()
	push := func(p string) {
		if !seen[p] && len(add) < 1000 {
			seen[p] = true
			add = append(add, q.newStaged(filepath.Base(p), p, false, ""))
		}
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() || isPackage(p) {
			push(p)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if path != p && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$")) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != p && isPackage(path) {
					push(path)
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := extTable[strings.ToLower(filepath.Ext(name))]; ok {
				push(path)
			}
			return nil
		})
	}
	q.mu.Lock()
	q.staged = append(q.staged, add...)
	q.mu.Unlock()
	return len(add)
}

// isPackage: old-style .key/.pages/.numbers documents are folders.
func isPackage(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".key", ".pages", ".numbers":
		return true
	}
	return false
}

func (q *Queue) StagedList() []Staged {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Staged, len(q.staged))
	for i, s := range q.staged {
		out[i] = *s
	}
	return out
}

func (q *Queue) Unstage(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, s := range q.staged {
		if s.ID == id {
			if s.owned && s.dir != "" {
				os.RemoveAll(s.dir)
			}
			q.staged = append(q.staged[:i], q.staged[i+1:]...)
			return
		}
	}
}

func (q *Queue) ClearStaged() {
	q.mu.Lock()
	list := q.staged
	q.staged = nil
	q.mu.Unlock()
	for _, s := range list {
		if s.owned && s.dir != "" {
			os.RemoveAll(s.dir)
		}
	}
}

// ConvertStaged turns everything staged into a new batch.
func (q *Queue) ConvertStaged(opts Options) (*Batch, error) {
	return q.ConvertStagedAndSend(opts, "")
}

// ConvertStagedAndSend converts and, if sendTo is set, sends each PDF as it finishes.
func (q *Queue) ConvertStagedAndSend(opts Options, sendTo string) (*Batch, error) {
	opts = opts.normalized()
	q.mu.Lock()
	var list []*Staged
	ok := 0
	for _, s := range q.staged {
		if s.Error == "" {
			ok++
		}
	}
	if ok == 0 {
		q.mu.Unlock()
		return nil, errors.New("there's nothing to convert — add some files first")
	}
	list, q.staged = q.staged, nil
	q.mu.Unlock()

	b := &Batch{ID: newID(), Opts: opts, Created: time.Now(), adding: true, SendTo: sendTo}
	q.mu.Lock()
	q.batches[b.ID] = b
	q.mu.Unlock()
	for _, s := range list {
		if s.Error != "" {
			if s.owned && s.dir != "" {
				os.RemoveAll(s.dir)
			}
			continue
		}
		dir := s.dir
		if dir == "" {
			d, err := q.NewInboxDir()
			if err != nil {
				return nil, err
			}
			dir = d
		}
		id := newID()
		q.mu.Lock()
		b.JobIDs = append(b.JobIDs, id)
		q.mu.Unlock()
		q.Submit(id, dir, s.path, s.Name, s.owned, opts, "", b.ID)
	}
	q.mu.Lock()
	b.adding = false
	q.mu.Unlock()
	q.maybeMerge(b.ID)
	return b, nil
}

// ---------- jobs ----------

// Submit queues one file. Unsupported files show up as failed.
func (q *Queue) Submit(id, workDir, src, name string, owns bool, opts Options, outDir, batch string) *Job {
	j := &Job{
		ID: id, BatchID: batch, Name: name, Src: src, WorkDir: workDir, OwnsSrc: owns,
		Opts: opts.normalized(), OutDir: outDir, Created: time.Now(), done: make(chan struct{}),
	}
	p, err := planFor(name, q.avail)
	j.Plan = p
	q.mu.Lock()
	q.jobs[id] = j
	if err != nil {
		j.Status, j.Err, j.Finished = StatusFailed, err.Error(), time.Now()
		close(j.done)
		q.mu.Unlock()
		return j
	}
	j.Status = StatusQueued
	q.mu.Unlock()
	q.chans[p.Kind] <- j
	return j
}

func (q *Queue) setPhase(j *Job, phase string) {
	q.mu.Lock()
	j.Phase = phase
	q.mu.Unlock()
}

func (q *Queue) run(j *Job) {
	q.mu.Lock()
	j.Status, j.Started, j.Err, j.Phase = StatusConverting, time.Now(), "", "Converting with "+j.Plan.Kind.Label()
	q.mu.Unlock()

	ctx, cancel := context.WithTimeout(q.ctx, 15*time.Minute)
	tmp, err := q.conv.Convert(ctx, j)
	cancel()

	if err == nil && j.Opts.needsPost() {
		q.setPhase(j, "Adding finishing touches")
		polished := filepath.Join(j.WorkDir, "polished.pdf")
		if err = q.post(tmp, polished, j.Opts); err == nil {
			tmp = polished
		}
	}
	var dest string
	var size int64
	if err == nil {
		dest, size, err = q.finalize(j, tmp)
	}
	pages := 0
	if err == nil {
		pages = pageCount(dest)
	}

	q.mu.Lock()
	j.Finished, j.Phase = time.Now(), ""
	if err != nil {
		j.Status, j.Err = StatusFailed, err.Error()
		log.Printf("failed %s: %v", j.Name, err)
	} else {
		j.Status, j.OutPath, j.Size, j.Pages = StatusDone, dest, size, pages
		log.Printf("done %s -> %s (%s)", j.Name, dest, time.Since(j.Started).Round(time.Millisecond))
	}
	done := j.done
	q.mu.Unlock()

	if err == nil && j.WorkDir != "" {
		_ = os.RemoveAll(j.WorkDir) // keep failed inputs around for Retry
	}
	if err == nil && j.OutDir == "" {
		q.afterDone(j.ID, j.BatchID)
	}
	q.saveLibrary()
	close(done)
	q.maybeMerge(j.BatchID)
}

func (q *Queue) finalize(j *Job, tmp string) (string, int64, error) {
	dir := j.OutDir
	if dir == "" {
		dir = filepath.Join(q.paths.Converted, j.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	dest := uniquePath(dir, j.Opts.Prefix+pdfName(j.Name))
	var err error
	if tmp == j.Src && !j.OwnsSrc {
		err = copyFile(tmp, dest)
	} else {
		err = moveFile(tmp, dest)
	}
	if err != nil {
		return "", 0, err
	}
	return dest, fileSize(dest), nil
}

// maybeMerge builds the combined PDF once every file in a batch has finished.
func (q *Queue) maybeMerge(batchID string) {
	q.mu.Lock()
	b, ok := q.batches[batchID]
	if !ok || !b.Opts.Merge || b.merged || b.adding {
		q.mu.Unlock()
		return
	}
	var files []string
	for _, id := range b.JobIDs {
		j := q.jobs[id]
		if j == nil {
			q.mu.Unlock()
			return // still being submitted
		}
		if j.Status == StatusQueued || j.Status == StatusConverting {
			q.mu.Unlock()
			return
		}
		if j.Status == StatusDone && !j.Combined {
			files = append(files, j.OutPath)
		}
	}
	b.merged = true
	if len(files) < 2 {
		q.mu.Unlock()
		return
	}
	now := time.Now()
	mj := &Job{
		ID: newID(), BatchID: b.ID, Name: b.Opts.MergeName + ".pdf", Plan: plan{Kind: KindPDF},
		Opts: b.Opts, Combined: true, Status: StatusConverting,
		Phase:   "Combining " + strconv.Itoa(len(files)) + " PDFs",
		Created: now.Add(time.Millisecond), Started: now, done: make(chan struct{}),
	}
	q.jobs[mj.ID] = mj
	b.JobIDs = append(b.JobIDs, mj.ID)
	q.mu.Unlock()

	go func() {
		dir := filepath.Join(q.paths.Converted, mj.ID)
		dest := filepath.Join(dir, b.Opts.Prefix+mj.Name)
		err := os.MkdirAll(dir, 0o755)
		if err == nil {
			err = q.merge(files, dest)
		}
		pages := 0
		if err == nil {
			pages = pageCount(dest)
		}
		q.mu.Lock()
		mj.Finished, mj.Phase = time.Now(), ""
		if err != nil {
			mj.Status, mj.Err = StatusFailed, "couldn't combine: "+err.Error()
		} else {
			mj.Status, mj.OutPath, mj.Size, mj.Pages = StatusDone, dest, fileSize(dest), pages
		}
		q.mu.Unlock()
		if err == nil {
			q.afterDone(mj.ID, mj.BatchID)
		}
		q.saveLibrary()
		close(mj.done)
	}()
}

func (q *Queue) Wait(j *Job) {
	q.mu.Lock()
	ch := j.done
	q.mu.Unlock()
	<-ch
}

func (q *Queue) Get(id string) (*Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	return j, ok
}

func (q *Queue) Busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.Status == StatusQueued || j.Status == StatusConverting {
			return true
		}
	}
	return false
}

func (q *Queue) Snapshot() []JobView {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]JobView, 0, len(q.jobs))
	now := time.Now()
	for _, j := range q.jobs {
		v := JobView{
			ID: j.ID, Batch: j.BatchID, Name: j.Name, OutName: filepath.Base(j.OutPath), Kind: j.Plan.Kind.Label(),
			Status: j.Status, Phase: j.Phase, Error: j.Err, Combined: j.Combined, Size: j.Size, Pages: j.Pages,
			SavedTo: j.SavedTo, SentTo: j.SentTo, Sending: j.Sending, SendError: j.SendErr, Created: j.Created.UnixMilli(),
		}
		v.CanRetry = j.Status == StatusFailed && j.Plan.Kind != "" && !j.Combined
		if j.OutPath == "" {
			v.OutName = j.Opts.Prefix + pdfName(j.Name)
		}
		if !j.Started.IsZero() {
			end := j.Finished
			if end.IsZero() || end.Before(j.Started) {
				end = now
			}
			v.ElapsedMs = end.Sub(j.Started).Milliseconds()
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Created == out[b].Created {
			return out[a].ID < out[b].ID
		}
		return out[a].Created > out[b].Created
	})
	return out
}

// LatestBatch returns the id of the newest batch made this session.
func (q *Queue) LatestBatch() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var best *Batch
	for _, b := range q.batches {
		if best == nil || b.Created.After(best.Created) {
			best = b
		}
	}
	if best == nil {
		return ""
	}
	return best.ID
}

func (q *Queue) Retry(id string) error {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok || j.Status != StatusFailed || j.Combined {
		q.mu.Unlock()
		return errors.New("nothing to retry")
	}
	if _, err := os.Stat(j.Src); err != nil {
		q.mu.Unlock()
		return errors.New("the original file is gone — add it again")
	}
	p, err := planFor(j.Name, q.avail)
	if err != nil {
		q.mu.Unlock()
		return err
	}
	_ = os.MkdirAll(j.WorkDir, 0o755)
	j.Plan, j.Status, j.Err, j.done = p, StatusQueued, "", make(chan struct{})
	q.mu.Unlock()
	q.chans[p.Kind] <- j
	return nil
}

// Remove forgets a finished job and deletes its files.
func (q *Queue) Remove(id string) error {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return errors.New("not found")
	}
	if j.Status == StatusQueued || j.Status == StatusConverting {
		q.mu.Unlock()
		return errors.New("still converting")
	}
	delete(q.jobs, id)
	q.mu.Unlock()
	if j.WorkDir != "" {
		_ = os.RemoveAll(j.WorkDir)
	}
	if j.OutDir == "" {
		_ = os.RemoveAll(filepath.Join(q.paths.Converted, j.ID))
	}
	q.saveLibrary()
	return nil
}

func (q *Queue) ClearFinished(batch string) {
	for _, v := range q.Snapshot() {
		if (batch == "" || v.Batch == batch) && (v.Status == StatusDone || v.Status == StatusFailed) {
			_ = q.Remove(v.ID)
		}
	}
}

// SaveToDownloads copies a finished PDF into the download folder.
func (q *Queue) SaveToDownloads(id string) (string, error) {
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok || j.Status != StatusDone {
		q.mu.Unlock()
		return "", errors.New("that PDF isn't ready")
	}
	src := j.OutPath
	q.mu.Unlock()
	dir := q.paths.Downloads
	if q.settings != nil {
		dir = q.settings.DownloadDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := uniquePath(dir, filepath.Base(src))
	if err := copyFile(src, dest); err != nil {
		return "", err
	}
	q.mu.Lock()
	j.SavedTo = dest
	q.mu.Unlock()
	q.saveLibrary()
	return dest, nil
}

// SaveAll saves every finished, not-yet-saved PDF of a batch ("" = all).
func (q *Queue) SaveAll(batch string) (int, error) {
	n := 0
	var firstErr error
	for _, v := range q.Snapshot() {
		if v.Status != StatusDone || v.SavedTo != "" || (batch != "" && v.Batch != batch) {
			continue
		}
		if _, err := q.SaveToDownloads(v.ID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// afterDone runs the "automatically" options for a finished PDF.
func (q *Queue) afterDone(id, batchID string) {
	if q.settings == nil {
		return
	}
	st := q.settings.Get()
	if st.AutoSave {
		if _, e := q.SaveToDownloads(id); e != nil {
			log.Printf("auto-save: %v", e)
		}
	}
	method := ""
	q.mu.Lock()
	if b := q.batches[batchID]; b != nil && b.SendTo != "" {
		method = b.SendTo
	}
	q.mu.Unlock()
	if method == "" && st.AutoSend && (st.SendTo == "usb" || st.SendTo == "cloud") {
		method = st.SendTo
	}
	if method == "" || q.rm == nil || (method == "cloud" && !q.rm.Connected()) {
		return
	}
	go func() {
		if e := q.Send(id, method); e != nil {
			log.Printf("send: %v", e)
		}
	}()
}

// Send puts a finished PDF on the reMarkable ("cloud" or "usb").
func (q *Queue) Send(id, method string) error {
	if q.rm == nil {
		return errors.New("reMarkable sending isn't set up")
	}
	q.mu.Lock()
	j, ok := q.jobs[id]
	if !ok || j.Status != StatusDone {
		q.mu.Unlock()
		return errors.New("that PDF isn't ready")
	}
	if j.Sending {
		q.mu.Unlock()
		return errors.New("already sending")
	}
	if j.SentTo != "" {
		q.mu.Unlock()
		return nil // already on the tablet
	}
	j.Sending, j.SendErr = true, ""
	path := j.OutPath
	q.mu.Unlock()

	ctx, cancel := context.WithTimeout(q.ctx, 5*time.Minute)
	defer cancel()
	var err error
	switch method {
	case "usb":
		err = q.rm.UploadUSB(ctx, path, filepath.Base(path))
	case "cloud":
		err = q.rm.UploadCloud(ctx, path, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	default:
		err = errors.New("pick how to send to your reMarkable in Options")
	}
	q.mu.Lock()
	j.Sending = false
	if err != nil {
		j.SendErr = err.Error()
	} else {
		j.SentTo, j.SendErr = method, ""
	}
	q.mu.Unlock()
	q.saveLibrary()
	if err == nil {
		log.Printf("sent %s to reMarkable via %s", filepath.Base(path), method)
	}
	return err
}

// SendAll sends every finished, not-yet-sent PDF of a batch ("" = all), one at a time.
func (q *Queue) SendAll(batch, method string) (int, error) {
	n := 0
	var firstErr error
	for _, v := range q.Snapshot() {
		if v.Status != StatusDone || v.SentTo != "" || v.Sending || (batch != "" && v.Batch != batch) {
			continue
		}
		if err := q.Send(v.ID, method); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// ---------- the library: finished PDFs survive restarts ----------

type libEntry struct {
	ID       string    `json:"id"`
	Batch    string    `json:"batch"`
	Name     string    `json:"name"`
	Kind     Kind      `json:"kind"`
	OutPath  string    `json:"outPath"`
	Size     int64     `json:"size"`
	Pages    int       `json:"pages"`
	Combined bool      `json:"combined,omitempty"`
	SavedTo  string    `json:"savedTo,omitempty"`
	SentTo   string    `json:"sentTo,omitempty"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
}

func (q *Queue) saveLibrary() {
	if !q.persist {
		return
	}
	q.mu.Lock()
	lib := []libEntry{}
	for _, j := range q.jobs {
		if j.Status == StatusDone && j.OutDir == "" {
			lib = append(lib, libEntry{j.ID, j.BatchID, j.Name, j.Plan.Kind, j.OutPath, j.Size, j.Pages, j.Combined, j.SavedTo, j.SentTo, j.Created, j.Started, j.Finished})
		}
	}
	q.mu.Unlock()
	if err := writeJSONAtomic(q.libPath, lib); err != nil {
		log.Printf("save library: %v", err)
	}
}

func (q *Queue) loadLibrary() {
	b, err := os.ReadFile(q.libPath)
	if err != nil {
		return
	}
	var lib []libEntry
	if json.Unmarshal(b, &lib) != nil {
		return
	}
	for _, e := range lib {
		if _, err := os.Stat(e.OutPath); err != nil {
			continue // PDF was deleted outside the app
		}
		done := make(chan struct{})
		close(done)
		q.jobs[e.ID] = &Job{
			ID: e.ID, BatchID: e.Batch, Name: e.Name, Plan: plan{Kind: e.Kind}, Status: StatusDone, OutPath: e.OutPath,
			Size: e.Size, Pages: e.Pages, Combined: e.Combined, SavedTo: e.SavedTo, SentTo: e.SentTo, Created: e.Created,
			Started: e.Started, Finished: e.Finished, done: done,
		}
	}
}

// wipeWork deletes every converted file and upload: nothing but settings is
// kept between launches.
func wipeWork(p Paths) {
	for _, d := range []string{p.Inbox, p.Converted} {
		_ = os.RemoveAll(d)
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.Remove(filepath.Join(p.State, "library.json"))
}

// cleanInbox removes scratch folders left over from a previous run.
func cleanInbox(p Paths) {
	entries, err := os.ReadDir(p.Inbox)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 10*time.Minute {
			_ = os.RemoveAll(filepath.Join(p.Inbox, e.Name()))
		}
	}
}
