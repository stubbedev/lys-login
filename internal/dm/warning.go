package dm

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/unix"
)

const journalStreamEnv = "JOURNAL_STREAM"

func newWarningLogger(stderr *os.File, journalStream string) *log.Logger {
	if isJournalStream(stderr, journalStream) {
		return log.New(stderr, "<4>", 0)
	}
	return log.New(stderr, "warning: ", log.LstdFlags|log.Lmsgprefix)
}

func isJournalStream(f *os.File, journalStream string) bool {
	var st unix.Stat_t
	if journalStream == "" || unix.Fstat(int(f.Fd()), &st) != nil {
		return false
	}
	return journalStream == fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}
