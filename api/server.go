package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"

	"github.com/etclabscore/open-etc-pool/storage"
	"github.com/etclabscore/open-etc-pool/util"
)


type ApiConfig struct {
	Enabled              bool   `json:"enabled"`
	Listen               string `json:"listen"`
	PoolCharts           string `json:"poolCharts"`
	PoolChartsNum        int64  `json:"poolChartsNum"`
	NetCharts            string `json:"netCharts"`
	NetChartsNum         int64  `json:"netChartsNum"`
	MinerChartsNum       int64  `json:"minerChartsNum"`
	MinerCharts          string `json:"minerCharts"`
	ShareCharts          string `json:"shareCharts"`
	ShareChartsNum       int64  `json:"shareChartsNum"`
	StatsCollectInterval string `json:"statsCollectInterval"`
	HashrateWindow       string `json:"hashrateWindow"`
	HashrateLargeWindow  string `json:"hashrateLargeWindow"`
	LuckWindow           []int  `json:"luckWindow"`
	Payments             int64  `json:"payments"`
	Blocks               int64  `json:"blocks"`
	PurgeOnly            bool   `json:"purgeOnly"`
	PurgeInterval        string `json:"purgeInterval"`
}

type ApiServer struct {
	config              *ApiConfig
	backend             *storage.RedisClient
	hashrateWindow      time.Duration
	hashrateLargeWindow time.Duration
	stats               atomic.Value
	miners              map[string]*Entry
	minersMu            sync.RWMutex
	statsIntv           time.Duration
}

type Entry struct {
	stats     map[string]interface{}
	updatedAt int64
}

func NewApiServer(cfg *ApiConfig, backend *storage.RedisClient) *ApiServer {
	hashrateWindow := util.MustParseDuration(cfg.HashrateWindow)
	hashrateLargeWindow := util.MustParseDuration(cfg.HashrateLargeWindow)
	return &ApiServer{
		config:              cfg,
		backend:             backend,
		hashrateWindow:      hashrateWindow,
		hashrateLargeWindow: hashrateLargeWindow,
		miners:              make(map[string]*Entry),
	}
}

func (s *ApiServer) Start() {
	if s.config.PurgeOnly {
		log.Printf("Starting API in purge-only mode")
	} else {
		log.Printf("Starting API on %v", s.config.Listen)
	}

	s.statsIntv = util.MustParseDuration(s.config.StatsCollectInterval)
	statsTimer := time.NewTimer(s.statsIntv)
	log.Printf("Set stats collect interval to %v", s.statsIntv)

	purgeIntv := util.MustParseDuration(s.config.PurgeInterval)
	purgeTimer := time.NewTimer(purgeIntv)
	log.Printf("Set purge interval to %v", purgeIntv)

	sort.Ints(s.config.LuckWindow)

	if s.config.PurgeOnly {
		s.purgeStale()
	} else {
		s.purgeStale()
		s.collectStats()
	}

	go func() {
		for {
			select {
			case <-statsTimer.C:
				if !s.config.PurgeOnly {
					s.collectStats()
				}
				statsTimer.Reset(s.statsIntv)
			case <-purgeTimer.C:
				s.purgeStale()
				purgeTimer.Reset(purgeIntv)
			}
		}
	}()
	
	go func() {
		c := cron.New()

		poolCharts := s.config.PoolCharts
		log.Printf("Pool charts config is :%v", poolCharts)
		c.AddFunc(poolCharts, func() {
			s.collectPoolCharts()
		})

		netCharts := s.config.NetCharts
		log.Printf("Net charts config is :%v", netCharts)
		c.AddFunc(netCharts, func() {
			s.collectnetCharts()
		})

		minerCharts := s.config.MinerCharts
		log.Printf("Miner charts config is :%v", minerCharts)
		c.AddFunc(minerCharts, func() {

			miners, err := s.backend.GetAllMinerAccount()
			if err != nil {
				log.Println("Get all miners account error: ", err)
			}
			for _, login := range miners {
				miner, _ := s.backend.CollectWorkersStats(s.hashrateWindow, s.hashrateLargeWindow, login)
				s.collectMinerCharts(login, miner["currentHashrate"].(int64), miner["hashrate"].(int64), miner["workersOnline"].(int64))
			}
		})
		///test share chart
		shareCharts := s.config.ShareCharts
		log.Printf("Share charts config is :%v", shareCharts)
		c.AddFunc(shareCharts, func() {
			miners, err := s.backend.GetAllMinerAccount()
			if err != nil {
				log.Println("Get all miners account error: ", err)
			}
			for _, login := range miners {
				miner, _ := s.backend.CollectWorkersStats(s.hashrateWindow, s.hashrateLargeWindow, login)
				s.collectshareCharts(login, miner["workersOnline"].(int64))
			}

		})

		c.Start()

	if !s.config.PurgeOnly {
		s.listen()
	}
}
	
func (s *ApiServer) collectPoolCharts() {
	ts := util.MakeTimestamp() / 1000
	now := time.Now()
	year, month, day := now.Date()
	hour, min, _ := now.Clock()
	t2 := fmt.Sprintf("%d-%02d-%02d %02d_%02d", year, month, day, hour, min)
	stats := s.getStats()
	hash := fmt.Sprint(stats["hashrate"])
	log.Println("Pool Hash is ", ts, t2, hash)
	err := s.backend.WritePoolCharts(ts, t2, hash)
	if err != nil {
		log.Printf("Failed to fetch pool charts from backend: %v", err)
		return
	}
}

func (s *ApiServer) collectnetCharts() {
	ts := util.MakeTimestamp() / 1000
	now := time.Now()
	year, month, day := now.Date()
	hour, min, _ := now.Clock()
	t2 := fmt.Sprintf("%d-%02d-%02d %02d_%02d", year, month, day, hour, min)
	//stats := s.getStats()
	//diff := fmt.Sprint(stats["difficulty"])
	nodes, erro := s.backend.GetNodeStates()
	if erro != nil {
		log.Printf("Failed to fetch Diff charts from backend: %v", erro)
		return
	}
	diff := fmt.Sprint(nodes[0]["difficulty"])
	log.Println("Difficulty Hash is ", ts, t2, diff)
	err := s.backend.WriteDiffCharts(ts, t2, diff)
	if err != nil {
		log.Printf("Failed to fetch Diff charts from backend: %v", err)
		return
	}
}

func (s *ApiServer) collectMinerCharts(login string, hash int64, largeHash int64, workerOnline int64) {
	ts := util.MakeTimestamp() / 1000
	now := time.Now()
	year, month, day := now.Date()
	hour, min, _ := now.Clock()
	t2 := fmt.Sprintf("%d-%02d-%02d %02d_%02d", year, month, day, hour, min)
	log.Println("Miner "+login+" Hash is", ts, t2, hash, largeHash)
	err := s.backend.WriteMinerCharts(ts, t2, login, hash, largeHash, workerOnline)
	if err != nil {
		log.Printf("Failed to fetch miner %v charts from backend: %v", login, err)
	}
}

func (s *ApiServer) collectshareCharts(login string, workerOnline int64) {
	ts := util.MakeTimestamp() / 1000
	now := time.Now()
	year, month, day := now.Date()
	hour, min, _ := now.Clock()
	t2 := fmt.Sprintf("%d-%02d-%02d %02d_%02d", year, month, day, hour, min)

	log.Println("Share chart is created", ts, t2)

	err := s.backend.WriteShareCharts(ts, t2, login, 0, 0, workerOnline)
	if err != nil {
		log.Printf("Failed to fetch miner %v charts from backend: %v", login, err)
	}
}

func (s *ApiServer) listen() {
	r := mux.NewRouter()
	r.HandleFunc("/api/stats", s.StatsIndex)
	r.HandleFunc("/api/miners", s.MinersIndex)
	r.HandleFunc("/api/blocks", s.BlocksIndex)
	r.HandleFunc("/api/payments", s.PaymentsIndex)
	r.HandleFunc("/api/accounts/{login:0x[0-9a-fA-F]{40}}", s.AccountIndex)
	r.NotFoundHandler = http.HandlerFunc(notFound)
	err := http.ListenAndServe(s.config.Listen, r)
	if err != nil {
		log.Fatalf("Failed to start API: %v", err)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
}

func (s *ApiServer) purgeStale() {
	start := time.Now()
	total, err := s.backend.FlushStaleStats(s.hashrateWindow, s.hashrateLargeWindow)
	if err != nil {
		log.Println("Failed to purge stale data from backend:", err)
	} else {
		log.Printf("Purged stale stats from backend, %v shares affected, elapsed time %v", total, time.Since(start))
	}
}

func (s *ApiServer) collectStats() {
	start := time.Now()
	stats, err := s.backend.CollectStats(s.hashrateWindow, s.config.Blocks, s.config.Payments)
	if err != nil {
		log.Printf("Failed to fetch stats from backend: %v", err)
		return
	}
	if len(s.config.LuckWindow) > 0 {
		stats["luck"], err = s.backend.CollectLuckStats(s.config.LuckWindow)
		if err != nil {
			log.Printf("Failed to fetch luck stats from backend: %v", err)
			return
		}
	}
	stats["netCharts"], err = s.backend.GetNetCharts(s.config.NetChartsNum)
	stats["poolCharts"], err = s.backend.GetPoolCharts(s.config.PoolChartsNum)
	s.stats.Store(stats)
	log.Printf("Stats collection finished %s", time.Since(start))
}

func (s *ApiServer) StatsIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	reply := make(map[string]interface{})
	nodes, err := s.backend.GetNodeStates()
	if err != nil {
		log.Printf("Failed to get nodes stats from backend: %v", err)
	}
	reply["nodes"] = nodes

	stats := s.getStats()
	if stats != nil {
		reply["now"] = util.MakeTimestamp()
		reply["stats"] = stats["stats"]
		reply["poolCharts"] = stats["poolCharts"]
		reply["hashrate"] = stats["hashrate"]
		reply["minersTotal"] = stats["minersTotal"]
		reply["maturedTotal"] = stats["maturedTotal"]
		reply["immatureTotal"] = stats["immatureTotal"]
		reply["candidatesTotal"] = stats["candidatesTotal"]
		reply["netCharts"] = stats["netCharts"]
		reply["workersTotal"] = stats["workersTotal"]
	}

	err = json.NewEncoder(w).Encode(reply)
	if err != nil {
		log.Println("Error serializing API response: ", err)
	}
}

func (s *ApiServer) MinersIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	reply := make(map[string]interface{})
	stats := s.getStats()
	if stats != nil {
		reply["now"] = util.MakeTimestamp()
		reply["miners"] = stats["miners"]
		reply["hashrate"] = stats["hashrate"]
		reply["minersTotal"] = stats["minersTotal"]
	}

	err := json.NewEncoder(w).Encode(reply)
	if err != nil {
		log.Println("Error serializing API response: ", err)
	}
}

func (s *ApiServer) BlocksIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	reply := make(map[string]interface{})
	stats := s.getStats()
	if stats != nil {
		reply["matured"] = stats["matured"]
		reply["maturedTotal"] = stats["maturedTotal"]
		reply["immature"] = stats["immature"]
		reply["immatureTotal"] = stats["immatureTotal"]
		reply["candidates"] = stats["candidates"]
		reply["candidatesTotal"] = stats["candidatesTotal"]
		reply["luck"] = stats["luck"]
		reply["luckCharts"] = stats["luckCharts"]
	}

	err := json.NewEncoder(w).Encode(reply)
	if err != nil {
		log.Println("Error serializing API response: ", err)
	}
}

func (s *ApiServer) PaymentsIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	reply := make(map[string]interface{})
	stats := s.getStats()
	if stats != nil {
		reply["payments"] = stats["payments"]
		reply["paymentsTotal"] = stats["paymentsTotal"]
	}

	err := json.NewEncoder(w).Encode(reply)
	if err != nil {
		log.Println("Error serializing API response: ", err)
	}
}

func (s *ApiServer) AccountIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")

	login := strings.ToLower(mux.Vars(r)["login"])
	s.minersMu.Lock()
	defer s.minersMu.Unlock()

	reply, ok := s.miners[login]
	now := util.MakeTimestamp()
	cacheIntv := int64(s.statsIntv / time.Millisecond)
	// Refresh stats if stale
	if !ok || reply.updatedAt < now-cacheIntv {
		exist, err := s.backend.IsMinerExists(login)
		if !exist {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("Failed to fetch stats from backend: %v", err)
			return
		}

		stats, err := s.backend.GetMinerStats(login, s.config.Payments)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("Failed to fetch stats from backend: %v", err)
			return
		}
		workers, err := s.backend.CollectWorkersStats(s.hashrateWindow, s.hashrateLargeWindow, login)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("Failed to fetch stats from backend: %v", err)
			return
		}
		for key, value := range workers {
			stats[key] = value
		}
		stats["pageSize"] = s.config.Payments
	        stats["minerCharts"], err = s.backend.GetMinerCharts(s.config.MinerChartsNum, login)
		stats["shareCharts"], err = s.backend.GetShareCharts(s.config.ShareChartsNum, login)
		stats["paymentCharts"], err = s.backend.GetPaymentCharts(login)
		reply = &Entry{stats: stats, updatedAt: now}
		s.miners[login] = reply
	}

	w.WriteHeader(http.StatusOK)
	err := json.NewEncoder(w).Encode(reply.stats)
	if err != nil {
		log.Println("Error serializing API response: ", err)
	}
}

func (s *ApiServer) getStats() map[string]interface{} {
	stats := s.stats.Load()
	if stats != nil {
		return stats.(map[string]interface{})
	}
	return nil
}
