package tests

import (
	"bytes"
	"context"
	"fmt"
	"github.com/berty/go-orbit-db/orbitdb"
	"github.com/berty/go-orbit-db/stores/eventlogstore"
	"github.com/berty/go-orbit-db/stores/operation"
	. "github.com/smartystreets/goconvey/convey"
	"os"
	"path"
	"testing"
	"time"
)

func TestLogDatabase(t *testing.T) {
	ctx, _ := context.WithTimeout(context.Background(), time.Second*10)
	const dbPath = "./orbitdb/tests/eventlog"

	//.createInstance(ipfs, { directory: path.join(dbPath, '1') })
	Convey("creates and opens a database", t, FailureHalts, func(c C) {
		err := os.RemoveAll(dbPath)
		c.So(err, ShouldBeNil)

		db1Path := path.Join(dbPath, "1")
		db1IPFS := makeIPFS(ctx, t)
		infinity := -1

		orbitdb1, err := orbitdb.NewOrbitDB(ctx, db1IPFS, &orbitdb.NewOrbitDBOptions{
			Directory: &db1Path,
		})
		c.So(err, ShouldBeNil)

		defer orbitdb1.Close()

		c.Convey("basic tests", FailureHalts, func(c C) {
			db, err := orbitdb1.Log(ctx, "log database", nil)
			c.So(err, ShouldBeNil)
			c.So(db, ShouldNotBeNil)

			////// creates and opens a database
			c.So(db.Type(), ShouldEqual, "eventlog")
			c.So(db.DBName(), ShouldEqual, "log database")

			////// returns 0 items when it's a fresh database
			res := make(chan operation.Operation, 100)
			err = db.Stream(ctx, res, &eventlogstore.StreamOptions{Amount: &infinity})
			c.So(err, ShouldBeNil)
			c.So(len(res), ShouldEqual, 0)

			////// returns the added entry's hash, 1 entry
			op, err := db.Add(ctx, []byte("hello1"))
			c.So(err, ShouldBeNil)

			ops, err := db.List(ctx, &eventlogstore.StreamOptions{Amount: &infinity})

			c.So(err, ShouldBeNil)
			c.So(len(ops), ShouldEqual, 1)
			item := ops[0]

			c.So(item.GetEntry().Hash.String(), ShouldEqual, op.GetEntry().Hash.String())

			////// returns the added entry's hash, 2 entries
			err = db.Load(ctx, -1)
			c.So(err, ShouldBeNil)

			ops, err = db.List(ctx, &eventlogstore.StreamOptions{Amount: &infinity})
			c.So(err, ShouldBeNil)
			c.So(len(ops), ShouldEqual, 1)

			prevHash := ops[0].GetEntry().Hash

			op, err = db.Add(ctx, []byte("hello2"))
			c.So(err, ShouldBeNil)

			ops, err = db.List(ctx, &eventlogstore.StreamOptions{Amount: &infinity})
			c.So(err, ShouldBeNil)
			c.So(len(ops), ShouldEqual, 2)

			c.So(ops[1].GetEntry().Hash.String(), ShouldNotEqual, prevHash.String())
			c.So(ops[1].GetEntry().Hash.String(), ShouldEqual, op.GetEntry().Hash.String())
		})

		c.Convey("adds five items", FailureHalts, func(c C) {
			db, err := orbitdb1.Log(ctx, "second database", nil)
			c.So(err, ShouldBeNil)

			for i := 1; i <= 5; i++ {
				_, err := db.Add(ctx, []byte(fmt.Sprintf("hello%d", i)))
				c.So(err, ShouldBeNil)
			}

			items, err := db.List(ctx, &eventlogstore.StreamOptions{Amount: &infinity})
			c.So(err, ShouldBeNil)
			c.So(len(items), ShouldEqual, 5)

			for i := 1; i <= 5; i++ {
				c.So(string(items[i-1].GetValue()), ShouldEqual, fmt.Sprintf("hello%d", i))
			}
		})

		c.Convey("adds an item that is > 256 bytes", FailureHalts, func(c C) {
			db, err := orbitdb1.Log(ctx, "third database", nil)
			c.So(err, ShouldBeNil)

			msg := bytes.Repeat([]byte("a"), 1024)

			op, err := db.Add(ctx, msg)
			c.So(err, ShouldBeNil)
			c.So(op.GetEntry().Hash.String(), ShouldStartWith, "bafy")
		})

		c.Convey("iterator & collect & options", FailureHalts, func(c C) {
			itemCount := 5
			var ops []operation.Operation

			db, err := orbitdb1.Log(ctx, "iterator tests", nil)
			c.So(err, ShouldBeNil)

			for i := 0; i < itemCount; i++ {
				op, err := db.Add(ctx, []byte(fmt.Sprintf("hello%d", i)))
				c.So(err, ShouldBeNil)
				ops = append(ops, op)
			}

			c.Convey("iterator", FailureHalts, func(c C) {
				c.Convey("defaults", FailureHalts, func(c C) {
					c.Convey("returns an item with the correct structure", FailureHalts, func(c C) {
						ch := make(chan operation.Operation, 100)

						err = db.Stream(ctx, ch, nil)
						c.So(err, ShouldBeNil)

						next := <-ch

						c.So(next, ShouldNotBeNil)
						c.So(next.GetEntry().Hash.String(), ShouldStartWith, "bafy")
						c.So(next.GetKey(), ShouldBeNil)
						c.So(string(next.GetValue()), ShouldEqual, "hello4")
					})

					c.Convey("implements Iterator interface", FailureHalts, func(c C) {
						ch := make(chan operation.Operation, 100)

						err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &infinity})
						c.So(err, ShouldBeNil)

						c.So(len(ch), ShouldEqual, itemCount)
					})

					c.Convey("returns 1 item as default", FailureHalts, func(c C) {
						ch := make(chan operation.Operation, 100)

						err = db.Stream(ctx, ch, nil)
						c.So(err, ShouldBeNil)

						first := <-ch
						second := <-ch

						c.So(first.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
						c.So(second, ShouldEqual, nil)
						c.So(string(first.GetValue()), ShouldEqual, "hello4")
					})

					c.Convey("returns items in the correct order", FailureHalts, func(c C) {
						ch := make(chan operation.Operation, 100)

						amount := 3

						err := db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &amount})
						c.So(err, ShouldBeNil)

						i := len(ops) - amount

						for op := range ch {
							c.So(string(op.GetValue()), ShouldEqual, fmt.Sprintf("hello%d", i))
							i++
						}
					})
				})
			})

			c.Convey("collect", FailureHalts, func(c C) {
				c.Convey("returns all items", FailureHalts, func(c C) {
					messages, err := db.List(ctx, &eventlogstore.StreamOptions{Amount: &infinity})

					c.So(err, ShouldBeNil)
					c.So(len(messages), ShouldEqual, len(ops))
					c.So(string(messages[0].GetValue()), ShouldEqual, "hello0")
					c.So(string(messages[len(messages)-1].GetValue()), ShouldEqual, "hello4")
				})

				c.Convey("returns 1 item", FailureHalts, func(c C) {
					messages, err := db.List(ctx, nil)

					c.So(err, ShouldBeNil)
					c.So(len(messages), ShouldEqual, 1)
				})

				c.Convey("returns 3 items", FailureHalts, func(c C) {
					three := 3
					messages, err := db.List(ctx, &eventlogstore.StreamOptions{Amount: &three})

					c.So(err, ShouldBeNil)
					c.So(len(messages), ShouldEqual, 3)
				})
			})

			c.Convey("Options: limit", FailureHalts, func(c C) {
				c.Convey("returns 1 item when limit is 0", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					zero := 0
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &zero})
					c.So(err, ShouldBeNil)

					c.So(len(ch), ShouldEqual, 1)

					first := <-ch
					second := <-ch

					c.So(first.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
					c.So(second, ShouldBeNil)
				})

				c.Convey("returns 1 item when limit is 1", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					one := 1
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &one})
					c.So(err, ShouldBeNil)

					c.So(len(ch), ShouldEqual, 1)

					first := <-ch
					second := <-ch

					c.So(first.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
					c.So(second, ShouldBeNil)
				})

				c.Convey("returns 3 items", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					three := 3
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &three})
					c.So(err, ShouldBeNil)

					c.So(len(ch), ShouldEqual, 3)

					first := <-ch
					second := <-ch
					third := <-ch
					fourth := <-ch

					c.So(first.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-3].GetEntry().Hash.String())
					c.So(second.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-2].GetEntry().Hash.String())
					c.So(third.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
					c.So(fourth, ShouldBeNil)
				})

				c.Convey("returns all items", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &infinity})
					c.So(err, ShouldBeNil)

					c.So(len(ops), ShouldEqual, len(ch))

					var last operation.Operation
					for e := range ch {
						last = e
					}

					c.So(last.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
				})

				c.Convey("returns all items when limit is bigger than -1", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					minusThreeHundred := -300
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &minusThreeHundred})
					c.So(err, ShouldBeNil)

					c.So(len(ops), ShouldEqual, len(ch))

					var last operation.Operation
					for e := range ch {
						last = e
					}

					c.So(last.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
				})

				c.Convey("returns all items when limit is bigger than number of items", FailureHalts, func(c C) {
					ch := make(chan operation.Operation, 100)
					threeHundred := 300
					err = db.Stream(ctx, ch, &eventlogstore.StreamOptions{Amount: &threeHundred})
					c.So(err, ShouldBeNil)

					c.So(len(ops), ShouldEqual, len(ch))

					var last operation.Operation
					for e := range ch {
						last = e
					}

					c.So(last.GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
				})
			})

			c.Convey("Options: ranges", FailureHalts, func(c C) {
				c.Convey("gt & gte", FailureHalts, func(c C) {
					c.Convey("returns 1 item when gte is the head", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{GTE: &ops[len(ops)-1].GetEntry().Hash, Amount: &infinity})
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, 1)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
					})
					c.Convey("returns 0 items when gt is the head", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{GT: &ops[len(ops)-1].GetEntry().Hash, Amount: &infinity})
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, 0)
					})
					c.Convey("returns 2 item when gte is defined", FailureHalts, func(c C) {
						gte := ops[len(ops)-2].GetEntry().Hash

						messages, err := db.List(ctx, &eventlogstore.StreamOptions{GTE: &gte, Amount: &infinity})
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, 2)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-2].GetEntry().Hash.String())
						c.So(messages[1].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-1].GetEntry().Hash.String())
					})
					c.Convey("returns all items when gte is the root item", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{ GTE: &ops[0].GetEntry().Hash, Amount: &infinity })
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, len(ops))
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[0].GetEntry().Hash.String())
						c.So(messages[len(messages) - 1].GetEntry().Hash.String(), ShouldEqual, ops[len(ops) - 1].GetEntry().Hash.String())
					})
					c.Convey("returns items when gt is the root item", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{ GT: &ops[0].GetEntry().Hash, Amount: &infinity })
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, len(ops) - 1)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[1].GetEntry().Hash.String())
						c.So(messages[len(messages) - 1].GetEntry().Hash.String(), ShouldEqual, ops[len(ops) - 1].GetEntry().Hash.String())
					})
					c.Convey("returns items when gt is defined", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{ Amount: &infinity })
						c.So(err, ShouldBeNil)
						c.So(len(messages), ShouldEqual, 5)

						gt := messages[2].GetEntry().Hash
						hundred := 100

						messages2, err := db.List(ctx, &eventlogstore.StreamOptions{ GT: &gt, Amount: &hundred })
						c.So(err, ShouldBeNil)

						c.So(len(messages2), ShouldEqual, 2)
						c.So(messages2[0].GetEntry().Hash.String(), ShouldEqual, messages[len(messages) - 2].GetEntry().Hash.String())
						c.So(messages2[1].GetEntry().Hash.String(), ShouldEqual, messages[len(messages) - 1].GetEntry().Hash.String())
					})
				})

				c.Convey("lt & lte", FailureHalts, func(c C) {
					c.Convey("returns one item after head when lt is the head", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LT: &ops[len(ops)-1].GetEntry().Hash })
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, 1)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-2].GetEntry().Hash.String())
					})
					c.Convey("returns all items when lt is head and limit is -1", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LT: &ops[len(ops)-1].GetEntry().Hash, Amount: &infinity })
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, len(ops) - 1)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[0].GetEntry().Hash.String())
						c.So(messages[len(messages) - 1].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-2].GetEntry().Hash.String())
					})
					c.Convey("returns 3 items when lt is head and limit is 3", FailureHalts, func(c C) {
						three := 3
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LT: &ops[len(ops)-1].GetEntry().Hash, Amount: &three })
						c.So(err, ShouldBeNil)

						c.So(len(messages), ShouldEqual, 3)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-4].GetEntry().Hash.String())
						c.So(messages[2].GetEntry().Hash.String(), ShouldEqual, ops[len(ops)-2].GetEntry().Hash.String())
					})
					c.Convey("returns null when lt is the root item", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LT: &ops[0].GetEntry().Hash })
						c.So(err, ShouldBeNil)
						c.So(len(messages), ShouldEqual, 0)
					})
					c.Convey("returns one item when lte is the root item", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LTE: &ops[0].GetEntry().Hash })
						c.So(err, ShouldBeNil)
						c.So(len(messages), ShouldEqual, 1)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[0].GetEntry().Hash.String())
					})
					c.Convey("returns all items when lte is the head", FailureHalts, func(c C) {
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LTE: &ops[len(ops) - 1].GetEntry().Hash, Amount: &infinity })
						c.So(err, ShouldBeNil)
						c.So(len(messages), ShouldEqual, itemCount)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[0].GetEntry().Hash.String())
						c.So(messages[4].GetEntry().Hash.String(), ShouldEqual, ops[itemCount - 1].GetEntry().Hash.String())
					})
					c.Convey("returns 3 items when lte is the head", FailureHalts, func(c C) {
						three := 3
						messages, err := db.List(ctx, &eventlogstore.StreamOptions{LTE: &ops[len(ops) - 1].GetEntry().Hash, Amount: &three })
						c.So(err, ShouldBeNil)
						c.So(len(messages), ShouldEqual, three)
						c.So(messages[0].GetEntry().Hash.String(), ShouldEqual, ops[itemCount - 3].GetEntry().Hash.String())
						c.So(messages[1].GetEntry().Hash.String(), ShouldEqual, ops[itemCount - 2].GetEntry().Hash.String())
						c.So(messages[2].GetEntry().Hash.String(), ShouldEqual, ops[itemCount - 1].GetEntry().Hash.String())
					})
				})
			})
		})
	})
}
