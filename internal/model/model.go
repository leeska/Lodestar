// Package model defines the versioned wire contract and pure domain rules.
// It has no dependency on HTTP, SQL, collectors, or notification transports.
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"time"
)

// Version is overridden at release time with -ldflags "-X …/internal/model.Version=v1.0.0".
var Version = "3.0.0-dev"

const Protocol = 1

const OfflineAfter = 45 * time.Second

// FirstReportGrace is how long a node that has never reported may stay silent
// before the offline rule counts it: creating the node and installing its Agent
// are minutes apart, and a rule covering 全部节点 applies from the moment the
// node exists.
const FirstReportGrace = 15 * time.Minute

func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func percent(v float64) bool { return finite(v) && v >= 0 && v <= 100 }

// Env reads LODESTAR_<name>, falling back to the TZA_<name> spelling used
// before the project was renamed, so existing units and compose files keep
// working.
func Env(name string) string {
	if v := os.Getenv("LODESTAR_" + name); v != "" {
		return v
	}
	return os.Getenv("TZA_" + name)
}
