package main

import "time"

var (
	claudeCacheLockTimeout = 2 * time.Second
	codexCacheLockTimeout  = 2 * time.Second
	kimiCacheLockTimeout   = 2 * time.Second
	zaiCacheLockTimeout    = 2 * time.Second
)

func lockClaudeCache() (func(), error) {
	return lockProviderCache(".claude.lock", "Claude", claudeCacheLockTimeout)
}

func lockCodexCache() (func(), error) {
	return lockProviderCache(".codex.lock", "Codex", codexCacheLockTimeout)
}

func lockKimiCache() (func(), error) {
	return lockProviderCache(".kimi.lock", "Kimi", kimiCacheLockTimeout)
}

func lockZAICache() (func(), error) {
	return lockProviderCache(".zai.lock", "Z.AI", zaiCacheLockTimeout)
}
