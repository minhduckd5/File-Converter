// game.go
// Core game logic for Text-Based Clash Royale (TCR)

package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"sync"
	"time"
)

// GameMode indicates simple (turn-based) or enhanced (continuous)
type GameMode int

const (
	SimpleMode GameMode = iota
	EnhancedMode
)

// GameSession holds state for a single 1v1 match
// Comments: core state, channels, and synchronization primitives
// ========= STATE START =========
// mutex protects access to shared game state
var mutex sync.Mutex // protects session state during concurrent access

// GameSession struct encapsulates all session data
// use channels to receive deploy commands and stop signal
type GameSession struct {
	Mode         GameMode
	Players      [2]*Player // two players
	TroopSpecs   map[string]TroopSpec
	TowerSpecs   map[string]TowerSpec
	Commands     chan DeployCmd // incoming deploy commands
	Done         chan struct{}  // signals end of game
	TickInterval time.Duration  // for enhanced mode
}

// DeployCmd is issued by a client or AI to deploy a troop
type DeployCmd struct {
	PlayerIndex int    // 0 or 1
	TroopName   string // e.g., "Pawn"
}

// ========== STATE END ============

// startGame launches the appropriate game loop based on mode
func (gs *GameSession) startGame() {
	switch gs.Mode {
	case SimpleMode:
		gs.simpleLoop()
	case EnhancedMode:
		gs.enhancedLoop()
	}
}

// simpleLoop runs turn-based gameplay
func (gs *GameSession) simpleLoop() {
	current := rand.Intn(2) // random start; seed earlier
	for {
		select {
		case cmd := <-gs.Commands:
			if cmd.PlayerIndex != current {
				// ignore commands out of turn
				continue
			}
			// process deploy
			gs.handleDeploy(cmd)
			// if no tower destroyed, switch turn
			if !gs.justDestroyedTower {
				current = 1 - current
			}
			// check win condition
			if gs.checkGameEnd() {
				close(gs.Done)
				return
			}
		case <-gs.Done:
			return
		}
	}
}

// enhancedLoop runs real-time gameplay with mana regen and timeout
func (gs *GameSession) enhancedLoop() {
	ticker := time.NewTicker(gs.TickInterval) // regular ticks for mana and state updates【turn0search0】
	timeout := time.After(3 * time.Minute)    // 3-minute match timer【turn0search9】
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			gs.tick() // regen mana, tower attacks, send state
		case cmd := <-gs.Commands:
			gs.handleDeploy(cmd) // immediate deploy handling
		case <-timeout:
			gs.evaluateWinner()
			close(gs.Done)
			return
		case <-gs.Done:
			return
		}
	}
}

// tick handles periodic updates: mana regen and optional tower attacks
func (gs *GameSession) tick() {
	mutex.Lock() // protect shared updates【turn0search2】
	defer mutex.Unlock()
	for i := range gs.Players {
		p := gs.Players[i]
		if p.Mana < 10 {
			p.Mana++ // mana regen
		}
	}
	// optionally process tower auto-attacks here
	gs.broadcastState()
}

// handleDeploy processes a DeployCmd, checking mana and applying troop effects
func (gs *GameSession) handleDeploy(cmd DeployCmd) {
	mutex.Lock()
	defer mutex.Unlock()
	p := gs.Players[cmd.PlayerIndex]

	spec, ok := gs.TroopSpecs[cmd.TroopName] // stats lookup
	if !ok || p.Mana < spec.Mana {
		return // invalid or insufficient mana
	}
	p.Mana -= spec.Mana
	// apply troop action: attack or heal
	if cmd.TroopName == "Queen" {
		p.healWeakestTower(300)
	} else {
		gs.attackOpponentTower(cmd.PlayerIndex, spec)
	}
}

// attackOpponentTower resolves a troop attacking the next enemy tower
func (gs *GameSession) attackOpponentTower(idx int, spec TroopSpec) {
	target := gs.Players[1-idx].nextAliveTower()
	dmg := spec.ATK - target.DEF
	if dmg < 0 {
		dmg = 0
	}
	target.HP -= dmg
	if target.HP <= 0 {
		gs.Players[1-idx].destroyTower(target)
		gs.justDestroyedTower = true
	} else {
		gs.justDestroyedTower = false
	}
}

// broadcastState would serialize and send STATE_UPDATE to clients
func (gs *GameSession) broadcastState() {
	state := GameState{
		YourMana:       gs.Players[0].Mana,
		OpponentMana:   gs.Players[1].Mana,
		YourTowers:     make([]Tower, 0),
		OpponentTowers: make([]Tower, 0),
	}

	// Add player 0's towers
	for _, t := range gs.Players[0].Towers {
		if t.HP > 0 {
			state.YourTowers = append(state.YourTowers, Tower{
				Name: t.Name,
				HP:   t.HP,
			})
		}
	}

	// Add player 1's towers
	for _, t := range gs.Players[1].Towers {
		if t.HP > 0 {
			state.OpponentTowers = append(state.OpponentTowers, Tower{
				Name: t.Name,
				HP:   t.HP,
			})
		}
	}

	// Serialize and send state update
	data, err := json.Marshal(state)
	if err != nil {
		return
	}

	// Send to both players
	for _, p := range gs.Players {
		if p.Conn != nil {
			SendPDU(p.Conn, PDU{
				Type: "state_update",
				Data: data,
			})
		}
	}
}

// checkGameEnd returns true if a King Tower is destroyed
func (gs *GameSession) checkGameEnd() bool {
	for _, p := range gs.Players {
		if p.kingTowerDestroyed() {
			return true
		}
	}
	return false
}

// evaluateWinner compares towers on timeout and assigns EXP
func (gs *GameSession) evaluateWinner() {
	mutex.Lock()
	defer mutex.Unlock()

	// Count remaining towers for each player
	towers0 := 0
	towers1 := 0
	for _, t := range gs.Players[0].Towers {
		if t.HP > 0 {
			towers0++
		}
	}
	for _, t := range gs.Players[1].Towers {
		if t.HP > 0 {
			towers1++
		}
	}

	// Determine winner and assign EXP
	var winner, loser *Player
	if towers0 > towers1 {
		winner = gs.Players[0]
		loser = gs.Players[1]
	} else if towers1 > towers0 {
		winner = gs.Players[1]
		loser = gs.Players[0]
	} else {
		// Draw - both get small EXP
		for _, p := range gs.Players {
			if p.Conn != nil {
				SendPDU(p.Conn, PDU{
					Type: "game_end",
					Data: json.RawMessage(`{"result":"draw","exp":10}`),
				})
			}
		}
		return
	}

	// Send results to players
	if winner.Conn != nil {
		SendPDU(winner.Conn, PDU{
			Type: "game_end",
			Data: json.RawMessage(`{"result":"win","exp":30}`),
		})
	}
	if loser.Conn != nil {
		SendPDU(loser.Conn, PDU{
			Type: "game_end",
			Data: json.RawMessage(`{"result":"loss","exp":5}`),
		})
	}
}

// atomicallySavePlayers persists players.json safely【turn0search15】
func atomicSavePlayers(path string, data interface{}) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

// seed random once at startup to ensure varied gameplay【turn0search13】
func init() {
	rand.Seed(time.Now().UnixNano())
}
