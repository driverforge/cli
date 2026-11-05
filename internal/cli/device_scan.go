package cli

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/discovery"
)

// newDeviceScanCmd is a hidden diagnostic that fires an SSDP M-SEARCH for
// Control4 directors and dumps the replies. It needs no login — it's pure
// local-network discovery — and exists to (a) confirm controllers are reachable
// on this segment and (b) inspect what the USN/LOCATION actually contain, which
// determines how we match SSDP hits to graph devices.
//
//	driverforge device scan                 # find c4:director responders
//	driverforge device scan -v              # + interfaces probed and every raw packet
//	driverforge device scan --st ssdp:all   # is ANY SSDP device reachable from here?
func newDeviceScanCmd() *cobra.Command {
	var timeout time.Duration
	var st string
	cmd := &cobra.Command{
		Use:    "scan",
		Short:  "Probe the local network for Control4 controllers (SSDP)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := textOut(cmd)

			if flagVerbose {
				srcs, _ := discovery.Sources()
				fmt.Fprintf(out, "Probing from %d interface address(es):\n", len(srcs))
				for _, s := range srcs {
					fmt.Fprintf(out, "  %s\t%s\n", s.Iface, s.IP)
				}
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "Searching (SSDP ST=%s, %s)…\n\n", st, timeout)

			var raw int32
			var mu sync.Mutex
			var opts []discovery.Option
			if flagVerbose {
				opts = append(opts, discovery.WithPacketSink(func(src discovery.Source, from net.Addr, b []byte) {
					atomic.AddInt32(&raw, 1)
					mu.Lock()
					defer mu.Unlock()
					fmt.Fprintf(out, "── packet on %s from %s ──\n%s\n\n", src.Iface, from, strings.TrimRight(string(b), "\r\n"))
				}))
			}

			found, err := discovery.Search(cmd.Context(), st, timeout, opts...)
			if err != nil {
				return fmt.Errorf("ssdp search: %w", err)
			}
			if flagVerbose {
				fmt.Fprintf(out, "(%d raw datagram(s) received)\n\n", atomic.LoadInt32(&raw))
			}

			doc := scanOut{Responders: []responderOut{}}
			for _, c := range found {
				doc.Responders = append(doc.Responders, responderOut{Host: c.Host, USN: c.USN, Server: c.Server, Location: c.Location})
			}
			return emit(cmd, doc, func(w io.Writer) error {
				if len(found) == 0 {
					fmt.Fprintf(w, "No responders for ST=%s on this network segment.\n", st)
					return nil
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "HOST\tUSN\tSERVER\tLOCATION")
				for _, c := range found {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", dash(c.Host), dash(c.USN), dash(c.Server), dash(c.Location))
				}
				return tw.Flush()
			})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 4*time.Second, "how long to listen for SSDP advertisements")
	cmd.Flags().StringVar(&st, "st", discovery.SearchTargetC4Director, "SSDP search target (try ssdp:all to see any responder)")
	return cmd
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type scanOut struct {
	Responders []responderOut `json:"responders"`
}

// responderOut is one SSDP reply.
type responderOut struct {
	Host     string `json:"host"`
	USN      string `json:"usn"`
	Server   string `json:"server"`
	Location string `json:"location"`
}
