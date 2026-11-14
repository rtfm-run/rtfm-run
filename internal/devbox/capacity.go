package devbox

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// startMu serializes the capacity check and the start within a process so
// parallel workers can't both squeeze past the limit.
var startMu sync.Mutex

// reserveMiB is memory kept free for the host itself.
const reserveMiB = 4096

// MaxBoxes is the host-wide limit on running boxes: RTFM_MAX_BOXES if
// set, otherwise as many boxes of perBoxMiB as fit in the host's memory
// after a reserve (at least 1).
func MaxBoxes(perBoxMiB int) int {
	if v := os.Getenv("RTFM_MAX_BOXES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	total := memTotalMiB()
	if total <= 0 || perBoxMiB <= 0 {
		return 4
	}
	return max(1, (total-reserveMiB)/perBoxMiB)
}

func memTotalMiB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			return kb / 1024
		}
	}
	return 0
}

// checkCapacity refuses to start another box when the host's limit is
// reached. Boxes from every rtfm process on the host count (they all
// carry the devbox label). Call with startMu held.
func checkCapacity(ctx context.Context, bin string, perBoxMiB int) error {
	out, err := exec.CommandContext(ctx, bin, "ps", "-q", "--filter", "label=rtfm.run=devbox").Output()
	if err != nil {
		return fmt.Errorf("devbox: count running boxes: %w", err)
	}
	running := len(strings.Fields(string(out)))
	if limit := MaxBoxes(perBoxMiB); running >= limit {
		return fmt.Errorf("devbox: %d boxes already running on this host (limit %d for its memory; set RTFM_MAX_BOXES to change)", running, limit)
	}
	return nil
}

// parseMemMiB parses docker-style sizes ("4g", "512m"); empty means unknown.
func parseMemMiB(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1024, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		s = strings.TrimSuffix(s, "m")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n * mult
}
