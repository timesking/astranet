package route

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenhotels/astranet/skykiss"
)

type rCacheKey struct {
	sname uint64
	s     Selector
	r     Reducer
}

type Registry struct {
	sMap    BTree2D
	rLock   sync.RWMutex
	rCond   sync.Cond
	rRev    uint64
	closed  uint64
	initCtl sync.Once

	sCache map[uint64]map[rCacheKey]RouteInfo
	sLock  sync.RWMutex
}

func (self *Registry) init() {
	self.initCtl.Do(func() {
		self.sMap = BTreeNew()
		self.rCond.L = self.rLock.RLocker()
		self.sCache = make(map[uint64]map[rCacheKey]RouteInfo)
	})
}

func (self *Registry) touch() {
	var nVer = atomic.AddUint64(&self.rRev, 1)

	self.sLock.Lock()
	if self.sCache[nVer] == nil {
		self.sCache[nVer] = make(map[rCacheKey]RouteInfo)
	}
	for v, _ := range self.sCache {
		if v < nVer {
			delete(self.sCache, v)
		}
	}
	self.sLock.Unlock()

	self.rCond.Broadcast()
}

func (self *Registry) Push(id uint64, srv RouteInfo, action ...func()) {
	self.init()
	var closed = atomic.LoadUint64(&self.closed)
	if closed > 0 {
		return
	}
	self.sMap.Put(id, srv, action...)
	self.touch()
}

func (self *Registry) Pop(id uint64, srv RouteInfo) {
	self.init()
	self.sMap.Delete(id, srv)
	self.touch()
}

func (self *Registry) DiscoverTimeout(
	r Selector, sname uint64,
	wait time.Duration,
	reducer Reducer,
) (srv RouteInfo, found bool) {
	self.init()

	var started = time.Now()
	var stopAt = started.Add(wait)
	self.rLock.RLock()
	for {
		var cKey = rCacheKey{sname, r, reducer}
		var cVer = atomic.LoadUint64(&self.rRev)
		self.sLock.RLock()
		var cGr = self.sCache[cVer]
		if cGr != nil {
			srv, found = cGr[cKey]
		}
		self.sLock.RUnlock()

		if found {
			self.rLock.RUnlock()
			break
		}

		var tPool = make([]RouteInfo, 0)
		self.sMap.ForEach2(sname, func(k2 RouteInfo) bool {
			tPool = append(tPool, k2)
			return false
		})

		if len(tPool) == 0 {
			var timeLeft = stopAt.Sub(time.Now())
			if timeLeft > 0 {
				skykiss.WaitTimeout(&self.rCond, timeLeft)
				continue
			}
		} else {
			self.rLock.RUnlock()
			if reducer != nil {
				tPool = reducer.Reduce(tPool)
			}
			var tIdx, shouldCache = r.Select(tPool)
			srv, found = tPool[tIdx], true

			if shouldCache {
				self.sLock.Lock()
				var cGr = self.sCache[cVer]
				if cGr != nil {
					cGr[cKey] = srv
				}
				self.sLock.Unlock()
			}

			break
		}
		self.rLock.RUnlock()
		break
	}

	return
}

func (self *Registry) Discover(
	r Selector,
	sname uint64,
	reducer Reducer,
) (RouteInfo, bool) {
	self.init()
	return self.DiscoverTimeout(r, sname, 0, reducer)
}

func (self *Registry) Sync(other *Registry, onAdd, onDelete func(uint64, RouteInfo)) {
	self.init()
	other.init()
	self.sMap.Sync(
		other.sMap,
		func(k1 uint64, k2 RouteInfo) {
			if onAdd != nil {
				onAdd(k1, k2)
			}
		}, func(k1 uint64, k2 RouteInfo) {
			if onDelete != nil {
				onDelete(k1, k2)
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
