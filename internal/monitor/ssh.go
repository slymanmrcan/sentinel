package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SSHJournal is an opt-in, bounded incremental journal reader, never a full-log scan.
// Raw entries do not leave this reader. Only recognized OpenSSH failures are emitted.
type SSHJournal struct {
	runner systemCommandRunner
	root   string
}
type SSHFailure struct {
	At          time.Time
	IP, Account string
}
type SSHBatch struct {
	Cursor, Status string
	Failures       []SSHFailure
}

var sshFailurePattern = regexp.MustCompile(`^Failed (?:password|publickey) for (?:invalid user )?(\S+) from (\S+) port [0-9]+`)

func NewSSHJournal(root string) *SSHJournal {
	return &SSHJournal{runner: execSystemCommandRunner{hostRoot: root, strictJournal: true}, root: root}
}
func (j *SSHJournal) Read(ctx context.Context, cursor string, now time.Time) SSHBatch {
	batch := SSHBatch{Cursor: cursor}
	args := []string{"--system", "--no-pager", "--output=json", "--output-fields=__CURSOR,__REALTIME_TIMESTAMP,MESSAGE,_COMM", "--unit=ssh.service", "--unit=sshd.service"}
	if cursor == "" {
		args = append(args, "--lines=1")
	} else {
		args = append(args, "--lines=201", "--after-cursor="+cursor, "--since=@"+strconv.FormatInt(now.Add(-2*time.Minute).Unix(), 10))
	}
	if j.root != "" {
		args = append(args, "--root="+j.root)
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := j.runner.Run(readCtx, "journalctl", args...)
	if err != nil {
		batch.Status = "Kullanılamıyor: journal erişimi / cursor okunamadı"
		batch.Cursor = ""
		return batch
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	seen := 0
	for _, line := range lines {
		var v map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		c := jsonString(v["__CURSOR"])
		if c == "" || len(c) > 2048 {
			continue
		}
		batch.Cursor = c
		seen++
		if cursor == "" || seen > 200 {
			continue
		} // Initial tail establishes position; no history flood.
		match := sshFailurePattern.FindStringSubmatch(jsonString(v["MESSAGE"]))
		if len(match) != 3 || (jsonString(v["_COMM"]) != "sshd" && jsonString(v["_COMM"]) != "sshd-session") {
			continue
		}
		ip, err := netip.ParseAddr(match[2])
		if err != nil {
			continue
		}
		micros, err := strconv.ParseInt(jsonString(v["__REALTIME_TIMESTAMP"]), 10, 64)
		if err != nil {
			continue
		}
		at := time.UnixMicro(micros)
		if now.Sub(at) > 2*time.Minute || at.After(now) {
			continue
		}
		hash := sha256.Sum256([]byte(match[1]))
		batch.Failures = append(batch.Failures, SSHFailure{at, ip.Unmap().String(), hex.EncodeToString(hash[:8])})
	}
	batch.Status = "Etkin: yalnızca OpenSSH başarısız parola/anahtar kayıtları"
	if cursor == "" && seen == 0 {
		batch.Status = "Kullanılamıyor: sistem SSH journal erişimi henüz doğrulanamadı (kayıt yok)"
	}
	if seen > 200 || len(output) >= systemCommandOutputLimit {
		batch.Status = "Kısmi: journal okuma sınırına ulaşıldı, bazı olaylar atlandı"
	}
	return batch
}
