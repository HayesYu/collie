package collie

import (
	"os"
	"strings"
)

const defaultForkRepo = "HayesYu/collie"

func forkReleaseRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = defaultForkRepo
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "https://github.com/" + strings.TrimPrefix(s, "/")
}

func init() {
	if v := strings.TrimSpace(os.Getenv("COLLIE_RELEASE_REPO")); v != "" {
		releaseRepoURL = forkReleaseRepoURL(v)
		return
	}
	releaseRepoURL = forkReleaseRepoURL(defaultForkRepo)
}
