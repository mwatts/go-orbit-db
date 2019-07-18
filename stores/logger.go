package stores

import "go.uber.org/zap"

func logger() *zap.Logger {
	return zap.L().Named("orbitdb.stores")
}
