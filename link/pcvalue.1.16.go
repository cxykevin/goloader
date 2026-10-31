//go:build go1.16 && !go1.28
// +build go1.16,!go1.28

package link

import "github.com/cxykevin/goloader/obj"

func adaptPCFile(linker *Linker, symbol *obj.ObjSymbol) {
}
