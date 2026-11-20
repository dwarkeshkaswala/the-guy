package pathutil

import "os"

func osMkdirAll(p string) error { return os.MkdirAll(p, 0o755) }
