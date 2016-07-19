package registry

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/joeshaw/gengen/generic"
	"github.com/zenhotels/astranet/skykiss"
	"github.com/zenhotels/btree-2d"
	"github.com/zenhotels/btree-2d/common"
)

type Registry struct {
	sMap    btree2d.BTree2D
	rLock   sync.RWMutex
	rCond   sync.Cond
	rRev    uint64
	closed  uint64
	initCtl sync.Once
}

func (self *Registry) init() {
	self.initCtl.Do(func() {
		self.sMap = btree2d.NewBTree2D()
		self.rCond.L = &self.rLock
	})
}

func (self *Registry) touch() {
	atomic.AddUint64(&self.rRev, 1)
	self.rCond.Broadcast()
}

func (self *Registry) Push(id generic.T, srv generic.U, action ...func()) {
	self.init()
	var closed = atomic.LoadUint64(&self.closed)
	if closed > 0 {
		return
	}
	var u = btree2d.NewFinalizable(&U{srv})
	for _, act := range action {
		u.AddFinalizer(act)
	}
	self.sMap.Put(&T{id}, u)
	self.touch()
}

func (self *Registry) Pop(id generic.T, srv generic.U) {
	self.init()
	self.sMap.Delete(&T{id}, &U{srv})
	self.touch()
}

func (self *Registry) DiscoverTimeout(r Selector, sname generic.T, wait time.Duration) (srv generic.U, found bool) {
	self.init()

	var started = time.Now()
	var stopAt = started.Add(wait)
	self.rLock.Lock()
	for {

		var tPool = make([]generic.U, 0)
		self.sMap.ForEach2(&T{sname}, func(k2 common.Comparable) bool {
			tPool = append(tPool, k2.(common.FinalizableComparable).Value().(*U).U)
			return false
		})

		if len(tPool) == 0 {
			var timeLeft = stopAt.Sub(time.Now())
			if timeLeft > 0 {
				skykiss.WaitTimeout(&self.rCond, timeLeft)
				continue
			}
		} else {
			srv, found = tPool[r.Select(tPool)], true
		}
		break

	}
	self.rLock.Unlock()

	return
}

func (self *Registry) Discover(r Selector, sname generic.T) (generic.U, bool) {
	self.init()
	return self.DiscoverTimeout(r, sname, 0)
}

func (self *Registry) Sync(other *Registry, onAdd, onDelete func(generic.T, generic.U)) {
	self.init()
	other.init()
	self.sMap.Sync(
		other.sMap,
		func(k1, k2 common.Comparable) {
			if onAdd != nil {
				onAdd(k1.(*T).T, k2.(common.FinalizableComparable).Value().(*U).U)
			}
		}, func(k1, k2 common.Comparable) {
			if onDelete != nil {
				onDelete(k1.(*T).T, k2.(common.FinalizableComparable).Value().(*U).U)
			}
		},
	)
}

func (self *Registry) Iter() Iterator {
	self.init()
	return Iterator{self, 0, time.Now()}
}

func (self *Registry) Close() {
	var keep = true
	for keep {
		var last = atomic.LoadUint64(&self.rRev)
		var clean Registry
		self.Sync(&clean, nil, nil)

		var swapped = atomic.CompareAndSwapUint64(&self.rRev, last, last)
		if swapped {
			atomic.AddUint64(&self.closed, 1)
		}
		keep = atomic.LoadUint64(&self.closed) == 0
	}
	self.touch()
}
