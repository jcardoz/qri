// Package logbook records and syncs dataset histories. As users work on
// datasets, they build of a log of operations. Each operation is a record
// of an action taken, like creating a dataset, or unpublishing a version.
// Each of these operations is wrtten to a log attributed to the user that
// performed the action, and stored in the logbook under the namespace of that
// dataset. The current state of a user's log is derived from iterating over
// all operations to produce the current state.
package logbook

import (
	"context"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	crypto "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/qri-io/dataset"
	"github.com/qri-io/qfs"
	"github.com/qri-io/qri/dsref"
	"github.com/qri-io/qri/identity"
	"github.com/qri-io/qri/logbook/oplog"
)

var (
	// ErrNoLogbook indicates a logbook doesn't exist
	ErrNoLogbook = fmt.Errorf("logbook: does not exist")
	// ErrNotFound is a sentinel error for data not found in a logbook
	ErrNotFound = fmt.Errorf("logbook: not found")
	// ErrLogTooShort indicates a log is missing elements. Because logs are
	// append-only, passing a shorter log than the one on file is grounds
	// for rejection
	ErrLogTooShort = fmt.Errorf("logbook: log is too short")

	// NewTimestamp generates the current unix nanosecond time.
	// This is mainly here for tests to override
	NewTimestamp = func() int64 { return time.Now().UnixNano() }
)

const (
	authorModel uint32 = iota
	nameModel
	branchModel
	versionModel
	publicationModel
	aclModel
	cronJobModel
)

// DefaultBranchName is the default name all branch-level logbook data is read
// from and written to. we currently don't present branches as a user-facing
// feature in qri, but logbook supports them
const DefaultBranchName = "main"

func modelString(m uint32) string {
	switch m {
	case authorModel:
		return "user"
	case nameModel:
		return "name"
	case branchModel:
		return "branch"
	case versionModel:
		return "version"
	case publicationModel:
		return "publication"
	case aclModel:
		return "acl"
	case cronJobModel:
		return "cronJob"
	default:
		return ""
	}
}

// Book wraps a oplog.Book with a higher-order API specific to Qri
type Book struct {
	bk         *oplog.Book
	pk         crypto.PrivKey
	fsLocation string
	fs         qfs.Filesystem
}

// NewBook initializes a logbook, reading any existing data at the given
// filesystem location. logbooks are encrypted at rest. The same key must be
// given to decrypt an existing logbook
func NewBook(pk crypto.PrivKey, username string, fs qfs.Filesystem, location string) (*Book, error) {
	ctx := context.Background()
	if pk == nil {
		return nil, fmt.Errorf("logbook: private key is required")
	}
	if fs == nil {
		return nil, fmt.Errorf("logbook: filesystem is required")
	}
	if location == "" {
		return nil, fmt.Errorf("logbook: location is required")
	}
	keyID, err := identity.KeyIDFromPriv(pk)
	if err != nil {
		return nil, err
	}

	bk, err := oplog.NewBook(pk, username, keyID)
	if err != nil {
		return nil, err
	}

	book := &Book{
		bk:         bk,
		fs:         fs,
		pk:         pk,
		fsLocation: location,
	}

	if err = book.load(ctx); err != nil {
		if err == ErrNotFound {
			err = book.initialize(ctx)
			return book, err
		}
		return nil, err
	}
	// else {
	// TODO (b5) verify username integrity on load
	// }

	return book, nil
}

func (book *Book) initialize(ctx context.Context) error {
	// initialize author's log of user actions
	userActions := oplog.InitLog(oplog.Op{
		Type:      oplog.OpTypeInit,
		Model:     authorModel,
		Name:      book.bk.AuthorName(),
		AuthorID:  book.bk.AuthorID(),
		Timestamp: NewTimestamp(),
	})
	book.bk.AppendLog(userActions)
	book.bk.SetAuthorID(userActions.ID())
	return book.save(ctx)
}

// ActivePeerID returns the in-use PeerID of the logbook author
func (book *Book) ActivePeerID() (id string) {
	lg, err := book.bk.Log(book.bk.AuthorID())
	if err != nil {
		panic(err)
	}
	return lg.Author()
}

// Author returns this book's author
func (book *Book) Author() oplog.Author {
	return book.bk
}

// AuthorName returns the human-readable name of the author
func (book *Book) AuthorName() string {
	return book.bk.AuthorName()
}

// RenameAuthor marks a change in author name
func (book *Book) RenameAuthor() error {
	return fmt.Errorf("not finished")
}

// DeleteAuthor removes an author, used on teardown
func (book *Book) DeleteAuthor() error {
	return fmt.Errorf("not finished")
}

// save writes the book to book.fsLocation
func (book *Book) save(ctx context.Context) error {

	ciphertext, err := book.bk.FlatbufferCipher()
	if err != nil {
		return err
	}

	file := qfs.NewMemfileBytes(book.fsLocation, ciphertext)
	book.fsLocation, err = book.fs.Put(ctx, file)
	return err
}

// load reads the book dataset from book.fsLocation
func (book *Book) load(ctx context.Context) error {
	f, err := book.fs.Get(ctx, book.fsLocation)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return ErrNotFound
		}
		return err
	}

	ciphertext, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}

	return book.bk.UnmarshalFlatbufferCipher(ctx, ciphertext)
}

// WriteNameInit initializes a new name within the author's namespace. Dataset
// histories start with a NameInit
// TODO (b5) - this presently only works for datasets in an author's user
// namespace
func (book *Book) WriteNameInit(ctx context.Context, name string) error {
	if book == nil {
		return ErrNoLogbook
	}
	book.initName(ctx, name)
	return book.save(ctx)
}

func (book Book) initName(ctx context.Context, name string) *oplog.Log {
	dsLog := oplog.InitLog(oplog.Op{
		Type:      oplog.OpTypeInit,
		Model:     nameModel,
		AuthorID:  book.bk.AuthorID(),
		Name:      name,
		Timestamp: NewTimestamp(),
	})

	branch := oplog.InitLog(oplog.Op{
		Type:      oplog.OpTypeInit,
		Model:     branchModel,
		AuthorID:  book.bk.AuthorID(),
		Name:      DefaultBranchName,
		Timestamp: NewTimestamp(),
	})

	dsLog.Logs = append(dsLog.Logs, branch)

	ns := book.authorLog()
	ns.Logs = append(ns.Logs, dsLog)
	return branch
}

func (book Book) authorLog() *oplog.Log {
	authorLog, err := book.bk.Log(book.bk.AuthorID())
	if err != nil {
		// this should never happen in practice
		// TODO (b5): create an author namespace on the spot if this happens
		panic(err)
	}
	return authorLog
}

// WriteNameAmend marks a rename event within a namespace
// TODO (b5) - finish
func (book *Book) WriteNameAmend(ctx context.Context, ref dsref.Ref, newName string) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.readDatasetLog(ref)
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:      oplog.OpTypeAmend,
		Model:     nameModel,
		Name:      newName,
		Timestamp: NewTimestamp(),
	})
	return nil
}

// WriteVersionSave adds an operation to a log marking the creation of a
// dataset version. Book will copy details from the provided dataset pointer
func (book *Book) WriteVersionSave(ctx context.Context, ds *dataset.Dataset) error {
	if book == nil {
		return ErrNoLogbook
	}

	ref := refFromDataset(ds)
	branchLog, err := book.BranchRef(ref)
	if err != nil {
		if err == oplog.ErrNotFound {
			branchLog = book.initName(ctx, ref.Name)
			err = nil
		} else {
			return err
		}
	}

	book.appendVersionSave(branchLog, ds)
	return book.save(ctx)
}

func (book *Book) appendVersionSave(l *oplog.Log, ds *dataset.Dataset) {
	op := oplog.Op{
		Type:  oplog.OpTypeInit,
		Model: versionModel,
		Ref:   ds.Path,
		Prev:  ds.PreviousPath,

		Timestamp: ds.Commit.Timestamp.UnixNano(),
		Note:      ds.Commit.Title,
	}

	if ds.Structure != nil {
		op.Size = int64(ds.Structure.Length)
	}

	l.Append(op)
}

// WriteVersionAmend adds an operation to a log amending a dataset version
func (book *Book) WriteVersionAmend(ctx context.Context, ds *dataset.Dataset) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.BranchRef(refFromDataset(ds))
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:  oplog.OpTypeAmend,
		Model: versionModel,
		Ref:   ds.Path,
		Prev:  ds.PreviousPath,

		Timestamp: ds.Commit.Timestamp.UnixNano(),
		Note:      ds.Commit.Title,
	})

	return book.save(ctx)
}

// WriteVersionDelete adds an operation to a log marking a number of sequential
// versions from HEAD as deleted. Because logs are append-only, deletes are
// recorded as "tombstone" operations that mark removal.
func (book *Book) WriteVersionDelete(ctx context.Context, ref dsref.Ref, revisions int) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.BranchRef(ref)
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:  oplog.OpTypeRemove,
		Model: versionModel,
		Size:  int64(revisions),
		// TODO (b5) - finish
	})

	return book.save(ctx)
}

// WritePublish adds an operation to a log marking the publication of a number
// of versions to one or more destinations
func (book *Book) WritePublish(ctx context.Context, ref dsref.Ref, revisions int, destinations ...string) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.BranchRef(ref)
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:      oplog.OpTypeInit,
		Model:     publicationModel,
		Size:      int64(revisions),
		Relations: destinations,
		// TODO (b5) - finish
	})

	return book.save(ctx)
}

// WriteUnpublish adds an operation to a log marking an unpublish request for a
// count of sequential versions from HEAD
func (book *Book) WriteUnpublish(ctx context.Context, ref dsref.Ref, revisions int, destinations ...string) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.BranchRef(ref)
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:      oplog.OpTypeRemove,
		Model:     publicationModel,
		Size:      int64(revisions),
		Relations: destinations,
		// TODO (b5) - finish
	})

	return book.save(ctx)
}

// WriteCronJobRan adds an operation to a log marking the execution of a cronjob
func (book *Book) WriteCronJobRan(ctx context.Context, number int64, ref dsref.Ref) error {
	if book == nil {
		return ErrNoLogbook
	}

	l, err := book.BranchRef(ref)
	if err != nil {
		return err
	}

	l.Append(oplog.Op{
		Type:  oplog.OpTypeInit,
		Model: cronJobModel,
		Size:  int64(number),
		// TODO (b5) - finish
	})

	return book.save(ctx)
}

// UserDatasetRef gets a user's log and a dataset reference, the returned log
// will be a user log with a single dataset log containing all known branches:
//   user
//     dataset
//       branch
//       branch
//       ...
func (book Book) UserDatasetRef(ref dsref.Ref) (*oplog.Log, error) {
	if ref.Username == "" {
		return nil, fmt.Errorf("logbook: reference Username is required")
	}
	if ref.Name == "" {
		return nil, fmt.Errorf("logbook: reference Name is required")
	}

	// fetch user log
	author, err := book.bk.HeadRef(ref.Username)
	if err != nil {
		return nil, err
	}

	// fetch dataset & all branches
	ds, err := book.bk.HeadRef(ref.Username, ref.Name)
	if err != nil {
		return nil, err
	}

	// construct a sparse oplog of just user, dataset, and branches
	return &oplog.Log{
		Ops:  author.Ops,
		Logs: []*oplog.Log{ds},
	}, nil
}

// DatasetRef gets a dataset log and all branches. Dataset logs describe
// activity affecting an entire dataset. Things like dataset name changes and
// access control changes are kept in the dataset log
//
// currently all logs are hardcoded to only accept one branch name. This
// function always returns
func (book Book) DatasetRef(ref dsref.Ref) (*oplog.Log, error) {
	if ref.Username == "" {
		return nil, fmt.Errorf("logbook: ref.Username is required")
	}
	if ref.Name == "" {
		return nil, fmt.Errorf("logbook: ref.Name is required")
	}

	return book.bk.HeadRef(ref.Username, ref.Name)
}

// BranchRef gets a branch log for a dataset reference. Branch logs describe
// a line of commits
//
// currently all logs are hardcoded to only accept one branch name. This
// function always returns
func (book Book) BranchRef(ref dsref.Ref) (*oplog.Log, error) {
	if ref.Username == "" {
		return nil, fmt.Errorf("logbook: ref.Username is required")
	}
	if ref.Name == "" {
		return nil, fmt.Errorf("logbook: ref.Name is required")
	}

	return book.bk.HeadRef(ref.Username, ref.Name, DefaultBranchName)
}

// Log gets a log for a given ID
func (book Book) Log(id string) (*oplog.Log, error) {
	return book.bk.Log(id)
}

// LogBytes signs a log with this book's private key and writes to a flatbuffer
func (book Book) LogBytes(log *oplog.Log) ([]byte, error) {
	return log.SignedFlatbufferBytes(book.pk)
}

// DsrefAliasForLog parses log data into a dataset alias reference, populating
// only the username and name components of a dataset.
// the passed in oplog must refer unambiguously to a dataset or branch.
// book.Log() returns exact log references
func DsrefAliasForLog(log *oplog.Log) (dsref.Ref, error) {
	ref := dsref.Ref{}
	if log == nil {
		return ref, fmt.Errorf("logbook: log is required")
	}
	if log.Model() != authorModel {
		return ref, fmt.Errorf("logbook: log isn't rooted as an author")
	}
	if len(log.Logs) != 1 {
		return ref, fmt.Errorf("logbook: ambiguous dataset reference")
	}

	ref = dsref.Ref{
		Username: log.Name(),
		Name:     log.Logs[0].Name(),
	}

	return ref, nil
}

// MergeLog adds a log to the logbook, merging with any existing log data
func (book *Book) MergeLog(ctx context.Context, sender oplog.Author, lg *oplog.Log) error {

	// eventually access control will dictate which logs can be written by whom.
	// For now we only allow users to merge logs they've written
	// book will need access to a store of public keys before we can verify
	// signatures non-same-senders
	if err := lg.Verify(sender.AuthorPubKey()); err != nil {
		return err
	}

	// if lg.ID() != sender.AuthorID() {
	// 	return fmt.Errorf("authors can only push logs they own")
	// }

	found, err := book.bk.Log(lg.ID())
	if err != nil {
		if err == oplog.ErrNotFound {
			book.bk.AppendLog(lg)
			return book.save(ctx)
		}
		return err
	}

	found.Merge(lg)
	return book.save(ctx)
}

// RemoveLog removes an entire log from a logbook
func (book *Book) RemoveLog(ctx context.Context, sender oplog.Author, ref dsref.Ref) error {
	l, err := book.BranchRef(ref)
	if err != nil {
		return err
	}

	// eventually access control will dictate which logs can be written by whom.
	// For now we only allow users to merge logs they've written
	// book will need access to a store of public keys before we can verify
	// signatures non-same-senders
	// if err := l.Verify(sender.AuthorPubKey()); err != nil {
	// 	return err
	// }

	root := l
	for {
		p := root.Parent()
		if p == nil {
			break
		}
		root = p
	}

	if root.ID() != sender.AuthorID() {
		return fmt.Errorf("authors can only remove logs they own")
	}

	book.bk.RemoveLog(dsRefToLogPath(ref)...)
	return book.save(ctx)
}

func dsRefToLogPath(ref dsref.Ref) (path []string) {
	for _, str := range []string{
		ref.Username,
		ref.Name,
	} {
		path = append(path, str)
	}
	return path
}

// ConstructDatasetLog creates a sparse log from a connected dataset history
// where no prior log exists
// the given history MUST be ordered from oldest to newest commits
// TODO (b5) - this presently only works for datasets in an author's user
// namespace
func (book *Book) ConstructDatasetLog(ctx context.Context, ref dsref.Ref, history []*dataset.Dataset) error {
	branchLog, err := book.BranchRef(ref)
	if err == nil {
		// if the log already exists, it will either as-or-more rich than this log,
		// refuse to overwrite
		return ErrLogTooShort
	}

	branchLog = book.initName(ctx, ref.Name)
	for _, ds := range history {
		book.appendVersionSave(branchLog, ds)
	}

	return book.save(ctx)
}

// DatasetInfo describes info aboud a dataset version in a repository
type DatasetInfo struct {
	Ref         dsref.Ref // version Reference
	Published   bool      // indicates whether this reference is listed as an available dataset
	Timestamp   time.Time // creation timestamp
	CommitTitle string    // title from commit
	Size        int64     // size of dataset in bytes
}

func infoFromOp(ref dsref.Ref, op oplog.Op) DatasetInfo {
	return DatasetInfo{
		Ref: dsref.Ref{
			Username:  ref.Username,
			ProfileID: ref.ProfileID,
			Name:      ref.Name,
			Path:      op.Ref,
		},
		Timestamp:   time.Unix(0, op.Timestamp),
		CommitTitle: op.Note,
		Size:        op.Size,
	}
}

// Versions plays a set of operations for a given log, producing a State struct
// that describes the current state of a dataset
func (book Book) Versions(ref dsref.Ref, offset, limit int) ([]DatasetInfo, error) {
	l, err := book.BranchRef(ref)
	if err != nil {
		return nil, err
	}

	refs := []DatasetInfo{}
	for _, op := range l.Ops {
		switch op.Model {
		case versionModel:
			switch op.Type {
			case oplog.OpTypeInit:
				refs = append(refs, infoFromOp(ref, op))
			case oplog.OpTypeAmend:
				refs[len(refs)-1] = infoFromOp(ref, op)
			case oplog.OpTypeRemove:
				refs = refs[:len(refs)-int(op.Size)]
			}
		case publicationModel:
			switch op.Type {
			case oplog.OpTypeInit:
				for i := 1; i <= int(op.Size); i++ {
					refs[len(refs)-i].Published = true
				}
			case oplog.OpTypeRemove:
				for i := 1; i <= int(op.Size); i++ {
					refs[len(refs)-i].Published = false
				}
			}
		}
	}

	// reverse the slice, placing newest first
	// https://github.com/golang/go/wiki/SliceTricks#reversing
	for i := len(refs)/2 - 1; i >= 0; i-- {
		opp := len(refs) - 1 - i
		refs[i], refs[opp] = refs[opp], refs[i]
	}

	if offset > len(refs) {
		offset = len(refs)
	}
	refs = refs[offset:]

	if limit < len(refs) {
		refs = refs[:limit]
	}

	return refs, nil
}

// LogEntry is a simplified representation of a log operation
type LogEntry struct {
	Timestamp time.Time
	Author    string
	Action    string
	Note      string
}

// String formats a LogEntry as a String
func (l LogEntry) String() string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", l.Timestamp.Format(time.Kitchen), l.Author, l.Action, l.Note)
}

// LogEntries returns a summarized "line-by-line" representation of a log for a
// given dataset reference
func (book Book) LogEntries(ctx context.Context, ref dsref.Ref, offset, limit int) ([]LogEntry, error) {
	l, err := book.BranchRef(ref)
	if err != nil {
		return nil, err
	}

	res := []LogEntry{}
	for _, op := range l.Ops {
		if offset > 0 {
			offset--
			continue
		}
		res = append(res, logEntryFromOp(ref.Username, op))
		if len(res) == limit {
			break
		}
	}

	return res, nil
}

var actionStrings = map[uint32][3]string{
	authorModel:      [3]string{"create profile", "update profile", "delete profile"},
	nameModel:        [3]string{"init dataset", "rename dataset", "delete dataset"},
	branchModel:      [3]string{"init branch", "rename branch", "delete branch"},
	versionModel:     [3]string{"save commit", "amend commit", "remove commit"},
	publicationModel: [3]string{"publish", "", "unpublish"},
	aclModel:         [3]string{"update access", "update access", "remove all access"},
	cronJobModel:     [3]string{"ran update", "", ""},
}

func logEntryFromOp(author string, op oplog.Op) LogEntry {
	note := op.Note
	if note == "" && op.Name != "" {
		note = op.Name
	}
	return LogEntry{
		Timestamp: time.Unix(0, op.Timestamp),
		Author:    author,
		Action:    actionStrings[op.Model][int(op.Type)-1],
		Note:      note,
	}
}

// RawLogs returns a serialized, complete set of logs keyed by model type logs
func (book Book) RawLogs(ctx context.Context) []Log {
	raw := book.bk.Logs()
	logs := make([]Log, len(raw))
	for i, l := range raw {
		logs[i] = newLog(l)
	}
	return logs
}

// Log is a human-oriented representation of oplog.Log intended for serialization
type Log struct {
	Ops  []Op  `json:"ops,omitempty"`
	Logs []Log `json:"logs,omitempty"`
}

func newLog(lg *oplog.Log) Log {
	ops := make([]Op, len(lg.Ops))
	for i, o := range lg.Ops {
		ops[i] = newOp(o)
	}

	var ls []Log
	if len(lg.Logs) > 0 {
		ls = make([]Log, len(lg.Logs))
		for i, l := range lg.Logs {
			ls[i] = newLog(l)
		}
	}

	return Log{
		Ops:  ops,
		Logs: ls,
	}
}

// Op is a human-oriented representation of oplog.Op intended for serialization
type Op struct {
	// type of operation
	Type string `json:"type,omitempty"`
	// data model to operate on
	Model string `json:"model,omitempty"`
	// identifier of data this operation is documenting
	Ref string `json:"ref,omitempty"`
	// previous reference in a causal history
	Prev string `json:"prev,omitempty"`
	// references this operation relates to. usage is operation type-dependant
	Relations []string `json:"relations,omitempty"`
	// human-readable name for the reference
	Name string `json:"name,omitempty"`
	// identifier for author
	AuthorID string `json:"authorID,omitempty"`
	// operation timestamp, for annotation purposes only
	Timestamp time.Time `json:"timestamp,omitempty"`
	// size of the referenced value in bytes
	Size int64 `json:"size,omitempty"`
	// operation annotation for users. eg: commit title
	Note string `json:"note,omitempty"`
}

func newOp(op oplog.Op) Op {
	return Op{
		Type:      opTypeString(op.Type),
		Model:     modelString(op.Model),
		Ref:       op.Ref,
		Prev:      op.Prev,
		Relations: op.Relations,
		Name:      op.Name,
		AuthorID:  op.AuthorID,
		Timestamp: time.Unix(0, op.Timestamp),
		Size:      op.Size,
		Note:      op.Note,
	}
}

func opTypeString(op oplog.OpType) string {
	switch op {
	case oplog.OpTypeInit:
		return "init"
	case oplog.OpTypeAmend:
		return "amend"
	case oplog.OpTypeRemove:
		return "remove"
	default:
		return ""
	}
}

func refFromDataset(ds *dataset.Dataset) dsref.Ref {
	return dsref.Ref{
		Username:  ds.Peername,
		ProfileID: ds.ProfileID,
		Name:      ds.Name,
		Path:      ds.Path,
	}
}

func (book Book) readDatasetLog(ref dsref.Ref) (*oplog.Log, error) {
	if ref.Username == "" {
		return nil, fmt.Errorf("ref.Username is required")
	}
	if ref.Name == "" {
		return nil, fmt.Errorf("ref.Name is required")
	}

	return book.bk.HeadRef(ref.Username, ref.Name)
}
