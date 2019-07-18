package basestore

import (
	ipfslog "berty.tech/go-ipfs-log"
	"berty.tech/go-ipfs-log/entry"
	"berty.tech/go-ipfs-log/identityprovider"
	"berty.tech/go-ipfs-log/io"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/berty/go-orbit-db/accesscontroller"
	"github.com/berty/go-orbit-db/accesscontroller/simple"
	"github.com/berty/go-orbit-db/address"
	"github.com/berty/go-orbit-db/ipfs"
	"github.com/berty/go-orbit-db/stores"
	"github.com/berty/go-orbit-db/stores/operation"
	"github.com/berty/go-orbit-db/stores/replicator"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"time"
)

type BaseStore struct {
	id                string
	identity          *identityprovider.Identity
	address           address.Address
	dbName            string
	ipfs              ipfs.Services
	cache             datastore.Datastore
	access            accesscontroller.Interface
	oplog             *ipfslog.Log
	replicator        replicator.Replicator
	storeType         string
	index             stores.Index
	replicationStatus replicator.ReplicationInfo
	loader            replicator.Replicator
	onClose           func(address.Address)
	stats             struct {
		snapshot struct {
			bytesLoaded int
		}
		syncRequestsReceived int
	}
	referenceCount int
	replicate      bool
	directory      string
	options        *stores.NewStoreOptions
	subscribers    []chan stores.Event
	cacheDestroy   func() error
}

func (b *BaseStore) DBName() string {
	return b.dbName
}

func (b *BaseStore) Ipfs() ipfs.Services {
	return b.ipfs
}

func (b *BaseStore) Identity() *identityprovider.Identity {
	return b.identity
}

func (b *BaseStore) OpLog() *ipfslog.Log {
	return b.oplog
}

func (b *BaseStore) AccessController() accesscontroller.Interface {
	return b.access
}

func (b *BaseStore) InitBaseStore(ctx context.Context, services ipfs.Services, identity *identityprovider.Identity, addr address.Address, options *stores.NewStoreOptions) error {
	var err error

	if identity == nil {
		return errors.New("identity required")
	}

	b.storeType = "store"
	b.id = addr.String()
	b.identity = identity
	b.address = addr
	b.dbName = addr.GetPath()
	b.ipfs = services
	b.cache = options.Cache
	b.cacheDestroy = options.CacheDestroy
	if options.AccessController != nil {
		b.access = options.AccessController
	} else {
		b.access = simple.NewSimpleAccessController(map[string][]string{"write": {identity.ID}})
	}

	b.oplog, err = ipfslog.NewLog(services, identity, &ipfslog.LogOptions{
		ID:               b.id,
		AccessController: b.access,
	})

	if err != nil {
		return errors.New("unable to instantiate an IPFS log")
	}

	if options.Index == nil {
		options.Index = NewBaseIndex
	}

	b.index = options.Index(b.identity.PublicKey)
	b.replicationStatus = replicator.NewReplicationInfo()

	b.stats.snapshot.bytesLoaded = -1

	replicatorChan := make(chan replicator.Event)

	b.replicator = replicator.NewReplicator(ctx, b, options.ReplicationConcurrency)
	b.replicator.Subscribe(replicatorChan)
	b.loader = b.replicator

	b.referenceCount = 64
	if options.ReferenceCount != nil {
		b.referenceCount = *options.ReferenceCount
	}

	b.directory = "./orbitdb"
	if options.Directory != "" {
		b.directory = options.Directory
	}

	b.replicate = true
	if options.Replicate != nil {
		b.replicate = *options.Replicate
	}

	b.options = options

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-replicatorChan:
				switch e.(type) {
				case *replicator.EventLoadAdded:
					// TODO
					//evt := e.(*replicator.EventLoadAdded)
					b.replicationLoadAdded(nil)
				}
				break
			}
		}
	}()

	return nil
}

func (b *BaseStore) replicationLoadAdded(e *entry.Entry) {
	// TODO
	//b.replicationStatus.IncQueued()
	//b.recalculateReplicationMax(e.Clock.Time)
	//// logger.debug(`<replicate>`)
	//b.emit(stores.NewEventReplicate(b.address, e))
}

func (b *BaseStore) Close() error {
	if b.onClose != nil {
		b.onClose(b.address)
	}

	// Replicator teardown logic
	b.replicator.Stop()

	// Reset replication statistics
	b.replicationStatus.Reset()

	// Reset database statistics
	b.stats.snapshot.bytesLoaded = -1
	b.stats.syncRequestsReceived = 0

	for _, s := range b.subscribers {
		s <- stores.NewEventClosed(b.address)
	}

	b.subscribers = []chan stores.Event{}

	err := b.cache.Close()
	if err != nil {
		return errors.Wrap(err, "unable to close cache")
	}

	return nil
}

func (b *BaseStore) Address() address.Address {
	return b.address
}

func (b *BaseStore) Index() stores.Index {
	return b.index
}

func (b *BaseStore) Type() string {
	return b.storeType
}

func (b *BaseStore) ReplicationStatus() replicator.ReplicationInfo {
	return b.replicationStatus
}

func (b *BaseStore) Drop() error {
	var err error
	if err = b.Close(); err != nil {
		return errors.Wrap(err, "unable to close store")
	}

	err = b.cacheDestroy()
	if err != nil {
		return errors.Wrap(err, "unable to destroy cache")
	}

	// TODO: Destroy cache? b.cache.Delete()

	// Reset
	b.index = b.options.Index(b.identity.PublicKey)
	b.oplog, err = ipfslog.NewLog(b.ipfs, b.identity, &ipfslog.LogOptions{
		ID:               b.id,
		AccessController: b.access,
	})

	if err != nil {
		return errors.Wrap(err, "unable to create log")
	}

	b.cache = b.options.Cache

	return nil
}

func (b *BaseStore) Load(ctx context.Context, amount int) error {
	if amount <= 0 && b.options.MaxHistory != nil {
		amount = *b.options.MaxHistory
	}

	var localHeads, remoteHeads []*entry.Entry
	localHeadsBytes, err := b.cache.Get(datastore.NewKey("_localHeads"))
	if err != nil {
		return errors.Wrap(err, "unable to get local heads from cache")
	}

	err = json.Unmarshal(localHeadsBytes, &localHeads)
	if err != nil {
		return errors.Wrap(err, "unable to unmarshal cached local heads")
	}

	remoteHeadsBytes, err := b.cache.Get(datastore.NewKey("_remoteHeads"))
	if err != nil && err != datastore.ErrNotFound {
		return errors.Wrap(err, "unable to get data from cache")
	}

	if remoteHeadsBytes != nil {
		err = json.Unmarshal(remoteHeadsBytes, &remoteHeads)
		if err != nil {
			return errors.Wrap(err, "unable to unmarshal cached remote heads")
		}
	}

	heads := append(localHeads, remoteHeads...)

	if len(heads) > 0 {
		b.emit(stores.NewEventLoad(b.address, heads))
	}

	for _, h := range heads {
		// TODO: parallelize things
		b.recalculateReplicationMax(h.Clock.Time)
		l, err := ipfslog.NewFromEntryHash(ctx, b.ipfs, b.identity, h.Hash, &ipfslog.LogOptions{
			ID:               b.oplog.ID,
			AccessController: b.access,
		}, &ipfslog.FetchOptions{
			Length:  &amount,
			Exclude: b.oplog.Values().Slice(),
			// TODO: ProgressChan:  this._onLoadProgress.bind(this),
		})

		if err != nil {
			return errors.Wrap(err, "unable to create log from entry hash")
		}

		l, err = b.oplog.Join(l, amount)
		if err != nil {
			return errors.Wrap(err, "unable to join log")
		}

		b.oplog = l
	}

	// Update the index
	if len(heads) > 0 {
		if err := b.updateIndex(); err != nil {
			return errors.Wrap(err, "unable to update index")
		}
	}

	b.emit(stores.NewEventReady(b.address, b.oplog.Heads().Slice()))
	return nil
}

func (b *BaseStore) Sync(ctx context.Context, heads []*entry.Entry) error {
	b.stats.syncRequestsReceived++

	if len(heads) == 0 {
		return nil
	}

	for _, h := range heads {
		if h == nil {
			//console.warn("Warning: Given input entry was 'null'.")
			continue
		}

		identityProvider := b.identity.Provider
		if identityProvider == nil {
			return errors.New("identity-provider is required, cannot verify entry")
		}

		canAppend := b.access.CanAppend(h, identityProvider)
		if canAppend != nil {
			//console.warn('Warning: Given input entry is not allowed in this log and was discarded (no write access).')
			continue
		}

		logEntry := h // TODO: copy?
		logEntry.Hash = cid.Cid{}

		hash, err := io.WriteCBOR(ctx, b.ipfs, logEntry)
		if err != nil {
			return errors.Wrap(err, "unable to write entry on dag")
		}

		if hash.String() != h.Hash.String() {
			//TODO: warn instead of error? console.warn('"WARNING! Head hash didn\'t match the contents')

			return errors.New("WARNING! Head hash didn't match the contents")
		}
	}

	return nil
}

func (b *BaseStore) LoadMoreFrom(ctx context.Context, amount uint, cids []cid.Cid) {
	b.replicator.Load(ctx, cids)
	// TODO: can this return an error?
}

type storeSnapshot struct {
	ID    string         `json:"id,omitempty"`
	Heads []*entry.Entry `json:"heads,omitempty"`
	Size  int            `json:"size,omitempty"`
	Type  string         `json:"type,omitempty"`
}

func (b *BaseStore) SaveSnapshot(ctx context.Context) (cid.Cid, error) {
	// I'd rather use protobuf here but I decided to keep the
	// JS behavior for the sake of compatibility across implementations

	unfinished := b.replicator.GetQueue()

	header, err := json.Marshal(&storeSnapshot{
		ID:    b.oplog.ID,
		Heads: b.oplog.Heads().Slice(),
		Size:  b.oplog.Values().Len(),
		Type:  b.storeType,
	})

	if err != nil {
		return cid.Cid{}, errors.Wrap(err, "unable to serialize snapshot")
	}

	headerSize := len(header)

	size := make([]byte, 2)
	binary.BigEndian.PutUint16(size, uint16(headerSize))
	rs := append(size, header...)

	for _, e := range b.oplog.Values().Slice() {
		entryJSON, err := json.Marshal(e)

		if err != nil {
			return cid.Cid{}, errors.Wrap(err, "unable to serialize entry as JSON")
		}

		logger().Debug(fmt.Sprintf("Serialized entry: %s", string(entryJSON)))

		size := make([]byte, 2)
		binary.BigEndian.PutUint16(size, uint16(len(entryJSON)))

		rs = append(rs, size...)
		rs = append(rs, entryJSON...)
	}

	rs = append(rs, 0)

	rsFileNode := files.NewBytesFile(rs)

	snapshotPath, err := b.ipfs.Unixfs().Add(ctx, rsFileNode)
	if err != nil {
		return cid.Cid{}, errors.Wrap(err, "unable to save log data on store")
	}

	err = b.cache.Put(datastore.NewKey("snapshot"), []byte(snapshotPath.Cid().String()))
	if err != nil {
		return cid.Cid{}, errors.Wrap(err, "unable to add snapshot data to cache")
	}

	unfinishedJSON, err := json.Marshal(unfinished)
	if err != nil {
		return cid.Cid{}, errors.Wrap(err, "unable to marshal unfinished cids")
	}

	err = b.cache.Put(datastore.NewKey("queue"), unfinishedJSON)
	if err != nil {
		return cid.Cid{}, errors.Wrap(err, "unable to add unfinished data to cache")
	}

	logger().Debug(fmt.Sprintf(`Saved snapshot: %s, queue length: %d`, snapshotPath.String(), len(unfinished)))

	return snapshotPath.Cid(), nil
}

func (b *BaseStore) LoadFromSnapshot(ctx context.Context) error {
	b.emit(stores.NewEventLoad(b.address, nil))

	queue, err := b.cache.Get(datastore.NewKey("queue"))
	if err != nil && err != datastore.ErrNotFound {
		return errors.Wrap(err, "unable to get value from cache")
	}

	_ = queue
	// TODO: unmarshal queue
	// TODO: this.sync(queue || [])

	snapshot, err := b.cache.Get(datastore.NewKey("snapshot"))
	if err == datastore.ErrNotFound {
		return errors.Wrap(err, "not found")
	}

	if err != nil {
		return errors.Wrap(err, "unable to get value from cache")
	}

	logger().Debug("loading snapshot from path", zap.String("snapshot", string(snapshot)))

	resNode, err := b.ipfs.Unixfs().Get(ctx, path.New(string(snapshot)))
	if err != nil {
		return errors.Wrap(err, "unable to get snapshot from ipfs")
	}

	res, ok := resNode.(files.File)
	if !ok {
		return errors.New("unable to cast fetched data as a file")
	}

	headerLengthRaw := make([]byte, 2)
	if _, err := res.Read(headerLengthRaw); err != nil {
		return errors.Wrap(err, "unable to read from stream")
	}

	headerLength := binary.BigEndian.Uint16(headerLengthRaw)
	header := &storeSnapshot{}
	headerRaw := make([]byte, headerLength)
	if _, err := res.Read(headerRaw); err != nil {
		return errors.Wrap(err, "unable to read from stream")
	}

	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return errors.Wrap(err, "unable to decode header from ipfs data")
	}

	var entries []*entry.Entry
	maxClock := 0

	for i := 0; i < header.Size; i++ {
		entryLengthRaw := make([]byte, 2)
		if _, err := res.Read(entryLengthRaw); err != nil {
			return errors.Wrap(err, "unable to read from stream")
		}

		entryLength := binary.BigEndian.Uint16(entryLengthRaw)
		e := &entry.Entry{}
		entryRaw := make([]byte, entryLength)

		if _, err := res.Read(entryRaw); err != nil {
			return errors.Wrap(err, "unable to read from stream")
		}

		logger().Debug(fmt.Sprintf("Entry raw: %s", string(entryRaw)))

		if err = json.Unmarshal(entryRaw, e); err != nil {
			return errors.Wrap(err, "unable to unmarshal entry from ipfs data")
		}

		entries = append(entries, e)
		if maxClock < e.Clock.Time {
			maxClock = e.Clock.Time
		}
	}

	b.recalculateReplicationMax(maxClock)

	var headsCids []cid.Cid
	for _, h := range header.Heads {
		headsCids = append(headsCids, h.Hash)
	}

	log, err := ipfslog.NewFromJSON(ctx, b.ipfs, b.identity, &ipfslog.JSONLog{
		Heads: headsCids,
		ID:    header.ID,
	}, &ipfslog.LogOptions{
		Entries:          entry.NewOrderedMapFromEntries(entries),
		ID:               header.ID,
		AccessController: b.access,
	}, &entry.FetchOptions{
		Length:  intPtr(-1),
		Timeout: time.Second,
	})

	if err != nil {
		return errors.Wrap(err, "unable to load log")
	}

	if _, err = b.oplog.Join(log, -1); err != nil {
		return errors.Wrap(err, "unable to join log")
	}

	if err := b.updateIndex(); err != nil {
		return errors.Wrap(err, "unable to update index")
	}

	return nil
}

func intPtr(i int) *int {
	return &i
}

func (b *BaseStore) AddOperation(ctx context.Context, op operation.Operation, onProgressCallback chan<- *entry.Entry) (*entry.Entry, error) {
	data, err := op.Marshal()
	if err != nil {
		return nil, errors.Wrap(err, "unable to marshal operation")
	}

	e, err := b.oplog.Append(ctx, data, b.referenceCount)
	if err != nil {
		return nil, errors.Wrap(err, "unable to append data on log")
	}
	b.recalculateReplicationStatus(b.replicationStatus.GetProgress()+1, e.Clock.Time)

	marshaledEntry, err := json.Marshal([]*entry.Entry{e})
	if err != nil {
		return nil, errors.Wrap(err, "unable to marshal entry")
	}

	err = b.cache.Put(datastore.NewKey("_localHeads"), marshaledEntry)
	if err != nil {
		return nil, errors.Wrap(err, "unable to add data to cache")
	}

	if err := b.updateIndex(); err != nil {
		return nil, errors.Wrap(err, "unable to update index")
	}

	b.emit(stores.NewEventWrite(b.address, e, b.oplog.Heads().Slice()))

	if onProgressCallback != nil {
		onProgressCallback <- e
	}

	return e, nil
}

func (b *BaseStore) recalculateReplicationProgress(max int) {
	if valuesLen := b.oplog.Values().Len(); b.replicationStatus.GetProgress() < valuesLen {
		b.replicationStatus.SetProgress(valuesLen)

	} else if b.replicationStatus.GetProgress() < max {
		b.replicationStatus.SetProgress(max)
	}

	b.recalculateReplicationMax(b.replicationStatus.GetProgress())
}

func (b *BaseStore) recalculateReplicationMax(max int) {
	if valuesLen := b.oplog.Values().Len(); b.replicationStatus.GetMax() < valuesLen {
		b.replicationStatus.SetMax(valuesLen)

	} else if b.replicationStatus.GetMax() < max {
		b.replicationStatus.SetMax(max)
	}
}

func (b *BaseStore) recalculateReplicationStatus(maxProgress, maxTotal int) {
	b.recalculateReplicationProgress(maxProgress)
	b.recalculateReplicationMax(maxTotal)
}

func (b *BaseStore) updateIndex() error {
	b.recalculateReplicationMax(0)
	if err := b.index.UpdateIndex(b.oplog, []*entry.Entry{}); err != nil {
		return errors.Wrap(err, "unable to update index")
	}
	b.recalculateReplicationProgress(0)

	return nil
}

func (b *BaseStore) emit(evt stores.Event) {
	for _, s := range b.subscribers {
		s <- evt
	}
}

func (b *BaseStore) Subscribe(c chan stores.Event) {
	for _, s := range b.subscribers {
		if s == c {
			return
		}
	}

	b.subscribers = append(b.subscribers, c)
}

func (b *BaseStore) Unsubscribe(c chan stores.Event) {
	for i, s := range b.subscribers {
		if s == c {
			b.subscribers[len(s)-1], b.subscribers[i] = b.subscribers[i], b.subscribers[len(s)-1]
			b.subscribers = b.subscribers[:len(s)-1]
			return
		}
	}
}

var _ stores.Interface = &BaseStore{}
