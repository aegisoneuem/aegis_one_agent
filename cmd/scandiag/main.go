//go:build windows

// scandiag is a throwaway diagnostic to understand why the WUA COM search
// disagrees with the Windows Update UI (UI shows missing updates, our scan
// returns 0). It runs the same search under several scopes/services and prints
// the count + titles + type for each, so we can see which scope actually
// surfaces the missing updates. Not part of the agent — investigation only.
package main

import (
	"fmt"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// ServerSelection enum: 0=ssDefault, 1=ssManagedServer, 2=ssWindowsUpdate, 3=ssOthers
const microsoftUpdateServiceID = "7971f918-a847-4430-9279-4a52d1efe18d"

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitialize(0); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != 0x00000001 {
			fmt.Println("CoInitialize failed:", err)
			return
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		fmt.Println("cannot create Update.Session:", err)
		return
	}
	defer unknown.Release()
	session, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		fmt.Println("QueryInterface failed:", err)
		return
	}
	defer session.Release()

	// A: exactly what the agent does today.
	runSearch(session, "A: default service, IsInstalled=0 and IsHidden=0",
		"IsInstalled=0 and IsHidden=0", -1, "")

	// B: drop the IsHidden filter, in case these are flagged hidden.
	runSearch(session, "B: default service, IsInstalled=0 (no hidden filter)",
		"IsInstalled=0", -1, "")

	// C: force the Windows Update service explicitly.
	runSearch(session, "C: ServerSelection=WindowsUpdate(2)",
		"IsInstalled=0 and IsHidden=0", 2, "")

	// D: force the Microsoft Update service (adds .NET/Defender/Office products).
	runSearch(session, "D: ServerSelection=Others(3) + Microsoft Update ServiceID",
		"IsInstalled=0 and IsHidden=0", 3, microsoftUpdateServiceID)

	// E: software-only, mirrors intended agent scope (exclude drivers).
	runSearch(session, "E: default service, software-only",
		"IsInstalled=0 and IsHidden=0 and Type='Software'", -1, "")
}

func runSearch(session *ole.IDispatch, label, criteria string, serverSel int, serviceID string) {
	fmt.Println("\n================================================================")
	fmt.Println(label)
	fmt.Printf("  criteria=%q serverSel=%d serviceID=%q\n", criteria, serverSel, serviceID)

	searcherRaw, err := oleutil.CallMethod(session, "CreateUpdateSearcher")
	if err != nil {
		fmt.Println("  CreateUpdateSearcher error:", err)
		return
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcher.Release()

	oleutil.PutProperty(searcher, "Online", true)
	if serverSel >= 0 {
		if _, err := oleutil.PutProperty(searcher, "ServerSelection", serverSel); err != nil {
			fmt.Println("  set ServerSelection error:", err)
		}
	}
	if serviceID != "" {
		if _, err := oleutil.PutProperty(searcher, "ServiceID", serviceID); err != nil {
			fmt.Println("  set ServiceID error:", err)
		}
	}

	resRaw, err := oleutil.CallMethod(searcher, "Search", criteria)
	if err != nil {
		fmt.Println("  Search error:", err)
		return
	}
	res := resRaw.ToIDispatch()
	defer res.Release()

	updatesRaw, err := oleutil.GetProperty(res, "Updates")
	if err != nil {
		fmt.Println("  get Updates error:", err)
		return
	}
	updates := updatesRaw.ToIDispatch()
	defer updates.Release()

	countRaw, err := oleutil.GetProperty(updates, "Count")
	if err != nil {
		fmt.Println("  get Count error:", err)
		return
	}
	count := int(countRaw.Val)
	fmt.Printf("  RESULT: %d update(s)\n", count)

	for i := 0; i < count; i++ {
		// Item is an indexed PROPERTY (propget), not a method — must use
		// GetProperty (DISPATCH_PROPERTYGET), not CallMethod (DISPATCH_METHOD),
		// or the access throws and every item is silently skipped.
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			fmt.Printf("    (item %d access error: %v)\n", i, err)
			continue
		}
		item := itemRaw.ToIDispatch()

		title, _ := oleutil.GetProperty(item, "Title")
		typeRaw, _ := oleutil.GetProperty(item, "Type") // 1=Software, 2=Driver
		isBetaRaw, _ := oleutil.GetProperty(item, "IsBeta")
		severity, _ := oleutil.GetProperty(item, "MsrcSeverity")

		kb := ""
		if kbRaw, err := oleutil.GetProperty(item, "KBArticleIDs"); err == nil {
			kbColl := kbRaw.ToIDispatch()
			if c, err := oleutil.GetProperty(kbColl, "Count"); err == nil && int(c.Val) > 0 {
				if kbItem, err := oleutil.GetProperty(kbColl, "Item", 0); err == nil {
					kb = "KB" + kbItem.ToString()
				}
			}
			kbColl.Release()
		}

		fmt.Printf("    - [%s] %q\n", kb, title.ToString())
		fmt.Printf("        Type=%v  Beta=%v  MsrcSeverity=%q\n",
			typeRaw.Value(), isBetaRaw.Value(), severity.ToString())
		printCategories(item)
		item.Release()
	}
}

// printCategories dumps the update's Categories collection so we can see the
// UpdateClassification (Security/Critical/Definition/Driver/...) and Product
// that enterprise UEM tools group by.
func printCategories(item *ole.IDispatch) {
	catsRaw, err := oleutil.GetProperty(item, "Categories")
	if err != nil {
		return
	}
	cats := catsRaw.ToIDispatch()
	defer cats.Release()

	countRaw, err := oleutil.GetProperty(cats, "Count")
	if err != nil {
		return
	}
	count := int(countRaw.Val)
	for i := 0; i < count; i++ {
		catRaw, err := oleutil.GetProperty(cats, "Item", i)
		if err != nil {
			continue
		}
		cat := catRaw.ToIDispatch()
		name, _ := oleutil.GetProperty(cat, "Name")
		catType, _ := oleutil.GetProperty(cat, "Type")
		fmt.Printf("        Category: %-22s (%s)\n", name.ToString(), catType.ToString())
		cat.Release()
	}
}
