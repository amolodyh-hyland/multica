package daemon

import "os"

func privateCacheReadFlags() int { return os.O_RDONLY }
