//go:build windows

package patch

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/go-ole/go-ole"
)

// sFalse (0x00000001) is the HRESULT CoInitialize returns when COM is already
// initialized on the current thread. It is a SUCCESS code, not a failure — but
// go-ole surfaces any non-zero HRESULT as an error whose message for code 1 is
// the misleading "Incorrect function". Treat it as success.
const sFalse = 0x00000001

// initCOM prepares the current goroutine for COM calls and returns a cleanup
// function to defer.
//
// It locks the goroutine to its OS thread first: COM's apartment state is
// per-thread, so if the goroutine migrated between CoInitialize and
// CoUninitialize (as it can across blocking calls and retries), CoUninitialize
// would run on the wrong thread and a later CoInitialize would report S_FALSE —
// exactly the spurious "CoInitialize failed: Incorrect function" seen during
// the scan retry loop.
func initCOM() (func(), error) {
	runtime.LockOSThread()

	if err := ole.CoInitialize(0); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != sFalse {
			runtime.UnlockOSThread()
			return nil, fmt.Errorf("CoInitialize failed: %v", err)
		}
		// S_FALSE: COM already initialized on this thread — fine, proceed.
	}

	return func() {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
	}, nil
}

// decimalVariantToFloat decodes a VT_DECIMAL VARIANT into a float64. WUA returns
// MaxDownloadSize as a DECIMAL, which go-ole does not expose via Value(). A
// DECIMAL is unioned over the VARIANT storage, so we read it from the VARIANT's
// raw bytes at the fixed OLE layout offsets:
//
//	[2]=scale  [3]=sign  [4:8]=Hi32  [8:12]=Lo32  [12:16]=Mid32
//
// The 96-bit magnitude is Hi32*2^64 + Mid32*2^32 + Lo32, divided by 10^scale.
func decimalVariantToFloat(v *ole.VARIANT) (float64, bool) {
	if v == nil || v.VT != ole.VT_DECIMAL {
		return 0, false
	}
	b := (*[16]byte)(unsafe.Pointer(v))
	scale := b[2]
	sign := b[3]
	hi32 := binary.LittleEndian.Uint32(b[4:8])
	lo32 := binary.LittleEndian.Uint32(b[8:12])
	mid32 := binary.LittleEndian.Uint32(b[12:16])

	val := float64(hi32)*18446744073709551616.0 + // 2^64
		float64(mid32)*4294967296.0 + // 2^32
		float64(lo32)
	for i := byte(0); i < scale; i++ {
		val /= 10
	}
	if sign&0x80 != 0 {
		val = -val
	}
	return val, true
}
