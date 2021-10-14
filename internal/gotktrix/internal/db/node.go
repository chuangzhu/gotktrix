package db

import (
	"log"
	"strings"

	"github.com/pkg/errors"
	"go.etcd.io/bbolt"
)

var ErrKeyNotFound = errors.New("key not found in database")

const nullKey = "\x00\x00"

func mustKey(key string) string {
	if key == "" {
		return nullKey
	}

	nulls := strings.Count(key, "\x00")
	if nulls == len(key) {
		if nulls >= 2 {
			// 2 or above, so add 1 more so that it's never 2.
			return key + "\x00"
		}
		// 1 null. Keep.
		return key
	}

	// No nulls. Return key.
	return key
}

type Node struct {
	kv   *KV
	txn  *bbolt.Tx
	buck *bbolt.Bucket
	path NodePath
}

// TxUpdate creates a new Node with an active transaction and calls f. If this
// method is called in a Node that already has a transaction, then that
// transaction is reused.
func (n Node) TxUpdate(f func(n Node) error) error {
	if n.txn != nil && !n.txn.Writable() {
		return bbolt.ErrTxNotWritable
	}

	return n.doTx(f, true)
}

// TxUpdate creates a new Node with an active read-only transaction and calls f.
// If this method is called in a Node that already has a transaction, then that
// transaction is reused.
func (n Node) TxView(f func(n Node) error) error {
	return n.doTx(f, false)
}

func (n *Node) doTx(f func(n Node) error, writable bool) error {
	if n.txn != nil {
		return f(*n)
	}

	t, err := n.kv.db.Begin(writable)
	if err != nil {
		return errors.Wrap(err, "failed to begin RO transaction")
	}
	defer t.Rollback()

	n.txn = t
	n.buck = nil

	if len(n.path) > 0 && writable {
		_, err := n.bucket()
		if err != nil {
			return errors.Wrap(err, "failed to fetch bucket for existing path")
		}
	}

	if err := f(*n); err != nil {
		return err
	}

	if writable {
		if err := n.txn.Commit(); err != nil {
			log.Println("commit error:", err)
			return errors.Wrap(err, "failed to commit to database")
		}
	}

	return nil
}

func (n *Node) bucket() (*bbolt.Bucket, error) {
	if n.buck != nil {
		return n.buck, nil
	}

	if n.txn == nil {
		return nil, bbolt.ErrTxClosed
	}

	b, err := n.path.Bucket(n.txn)
	if err != nil {
		if errors.Is(err, bbolt.ErrBucketNotFound) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}

	n.buck = b
	return b, nil
}

func (n *Node) bucketExists() bool {
	if n.buck != nil {
		return true
	}

	b, exists := n.path.BucketExists(n.txn)
	if exists {
		n.buck = b
	}

	return exists
}

// FromPath creates a new node with the given full path. The path will
// completely override the old path.
func (n Node) FromPath(path NodePath) Node {
	// Ensure that the given path does NOT get to grow further.
	n.path = path[:len(path):len(path)]
	n.buck = nil

	// Fill the bucket but skip over the error, since it doesn't really matter.
	n.bucket()

	return n
}

// Node creates a child node with the given names appended to its path. If the
// node has an ongoing transaction, then it is inherited over.
func (n Node) Node(names ...string) Node {
	if len(names) == 0 {
		panic("Node name can't be empty")
	}

	// if cap(n.path) > len(n.path)+len(names) {
	// 	// No growing required.
	// 	for _, name := range names {
	// 		n.path = append(n.path, []byte(name))
	// 	}
	// 	return n.FromPath(n.path)
	// }

	path := make([][]byte, len(n.path), (len(n.path)+len(names))*3/2)
	copy(path, n.path)

	for _, name := range names {
		path = append(path, []byte(name))
	}

	return n.FromPath(path)
}

// SetIfNone sets the key into the database only if the key does not exist. This
// method is useful primarily for filling up the cache with data fetched from
// the API while data from /sync should be prioritized.
func (n Node) SetIfNone(k string, v interface{}) error {
	bytes, err := n.kv.Marshal(v)
	if err != nil {
		return errors.Wrap(err, "Failed to marshal")
	}

	k = mustKey(k)

	return n.TxUpdate(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			return err
		}

		if b.Get([]byte(k)) != nil {
			return nil
		}

		return b.Put([]byte(k), bytes)
	})
}

// Set sets the key into the database.
func (n Node) Set(k string, v interface{}) error {
	bytes, err := n.kv.Marshal(v)
	if err != nil {
		return errors.Wrap(err, "failed to marshal")
	}

	k = mustKey(k)

	return n.TxUpdate(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			return err
		}

		return b.Put([]byte(k), bytes)
	})
}

// Exists returns true if the given key exists.
func (n Node) Exists(k string) (exists bool) {
	err := n.TxView(func(n Node) error {
		if !n.bucketExists() {
			return nil
		}

		b, err := n.bucket()
		if err != nil {
			return err
		}

		// If k is empty, then check for the bucket's presence.
		exists = k == "" || b.Get([]byte(k)) != nil
		return nil
	})

	return err == nil && exists
}

// Get gets the given key from the node.
func (n Node) Get(k string, v interface{}) error {
	k = mustKey(k)

	return n.TxView(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			return err
		}

		bytes := b.Get([]byte(k))
		if bytes == nil {
			return ErrKeyNotFound
		}

		if err := n.kv.Unmarshal(bytes, v); err != nil {
			return errors.Wrap(err, "failed to unmarshal")
		}

		return nil
	})
}

func (n Node) Delete(k string) error {
	k = mustKey(k)

	return n.TxUpdate(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			if errors.Is(err, ErrKeyNotFound) {
				// Already deleted.
				return nil
			}
			return err
		}
		return b.Delete([]byte(k))
	})
}

// Drop drops the entire node and all its values.
func (n Node) Drop() error {
	return n.TxUpdate(func(n Node) error {
		return dropBucketPrefix(n.txn, n.path)
	})
}

// DropExceptLast drops the entire node except for the last few values. This
// method heavily relies on keyed values being sorted properly, and that the
// stored values are NOT nested.
func (n Node) DropExceptLast(last int) error {
	return n.TxUpdate(func(n Node) error {
		var lastError error
		var buckets [][]byte

		b, err := n.bucket()
		if err != nil {
			return err
		}

		cursor := b.Cursor()

		for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
			if last > 0 {
				last--
				continue
			}

			if v == nil {
				buckets = append(buckets, append([]byte(nil), k...))
				continue
			}

			if err := cursor.Delete(); err != nil {
				lastError = err
			}
		}

		for _, k := range buckets {
			b.DeleteBucket(k)
		}

		return lastError
	})
}

// Length queries the number of keys within the node, similarly to running
// AllKeys and taking the length of what was returned.
func (n Node) Length(prefix string) (int, error) {
	// this will have a trailing delimiter regardless
	var length int

	return length, n.TxView(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			if errors.Is(err, ErrKeyNotFound) {
				// Ignore ErrKeyNotFound and just don't iterate.
				return nil
			}
			return err
		}

		cursor := b.Cursor()

		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			length++
		}

		return nil
	})
}

// EachBreak is an error that Each callbacks could return to stop the loop and
// return nil.
var EachBreak = errors.New("each break (not an error)")

// Each iterates over the bucket all possible keys with the prefix, or no
// prefix. It takes in a pointer.
//
// Caveats
//
// Since the pointer is reused, the user will need to manually copy it if they
// want to store the reference to that matched struct. Key includes the prefix.
//
// Example
//
// For iterating, as mentioned above, the user will need to manually copy the
// pointer by dereferencing and re-referencing it.
//
//    var obj Struct
//    var objs []Struct
//
//    n.Each(&obj, "", func(k string) error {
//        if obj.Thing == "what I want" {
//            objs = append(objs, obj)
//        }
//        return nil
//    })
//
func (n Node) Each(v interface{}, prefix string, fn func(k string, l int) error) error {
	return n.TxView(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			if errors.Is(err, ErrKeyNotFound) {
				// Ignore ErrKeyNotFound and just don't iterate.
				return nil
			}
			return err
		}

		cursor := b.Cursor()

		var length int
		for k, b := cursor.First(); k != nil; k, b = cursor.Next() {
			if b != nil {
				length++
			}
		}

		for k, b := cursor.First(); k != nil; k, b = cursor.Next() {
			if b == nil {
				continue // bucket
			}

			if err := n.kv.Unmarshal(b, v); err != nil {
				return errors.Wrapf(err, "failed to unmarshal %q", string(k))
			}

			if err := fn(string(k), length); err != nil {
				if err == EachBreak {
					return nil
				}
				return err
			}
		}

		return nil
	})
}

// EachKey iterates over keys.
func (n Node) EachKey(prefix string, fn func(k string, l int) error) error {
	return n.TxView(func(n Node) error {
		b, err := n.bucket()
		if err != nil {
			if errors.Is(err, ErrKeyNotFound) {
				// Ignore ErrKeyNotFound and just don't iterate.
				return nil
			}
			return err
		}

		cursor := b.Cursor()

		var length int
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			length++
		}

		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			if err := fn(string(k), length); err != nil {
				if err == EachBreak {
					return nil
				}
				return err
			}
		}

		return nil
	})
}
