// Command appxtest dumps the live MSIX/AppX/Store inventory. Dev diagnostic.
package main
import ("fmt"; "os"; "github.com/aegisoneuem/aegis_one_agent/internal/appxinventory")
func main() {
	pkgs, err := appxinventory.Collect()
	if err != nil { fmt.Fprintln(os.Stderr, "ERR:", err); os.Exit(1) }
	fmt.Printf("AppX/MSIX/Store packages: %d\n", len(pkgs))
	fmt.Printf("%-38s %-18s %-8s %-16s %s\n", "NAME", "VERSION", "ARCH", "ORIGIN", "DISPLAY")
	for _, p := range pkgs {
		n := p.Name; if len(n)>38 {n=n[:37]+"…"}
		fmt.Printf("%-38s v%-17s %-8s %-16s %s\n", n, p.Version, p.Architecture, p.Origin, p.DisplayName)
	}
}
