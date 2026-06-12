package update

import (
	"fmt"
	"io"
	"os"
)

// BannerFunc renders an "update available" notice to the user. The TUI and
// GUI packages override Banner from their init() to wire their own widgets;
// the default implementation writes a single line to stderr so headless
// builds (CI, scripts, --version probes) still surface the information.
type BannerFunc func(latest *Latest)

// Banner is invoked by the CLI after CheckOnce reports a newer release. A
// nil Latest is a no-op so callers can blindly forward CheckOnce's result.
var Banner BannerFunc = defaultBanner

// bannerOut is the stream the default banner writes to. Tests redirect it to
// a buffer; production code never touches it directly.
var bannerOut io.Writer = os.Stderr

func defaultBanner(latest *Latest) {
	if latest == nil {
		return
	}
	fmt.Fprintf(bannerOut, "Update available: %s — %s\n", latest.Tag, latest.URL)
}
