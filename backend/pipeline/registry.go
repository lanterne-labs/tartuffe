package pipeline

import (
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/plugins/photos"
	"digital-mirror/backend/plugins/twitter"
	"digital-mirror/backend/plugins/viepublique"
)

// Plugins is the run order. Ingest runs press sources before official
// records; backfill runs the cheap image catch-up before the GPU-bound
// transcript backfill so the latter cannot starve it.
var Plugins = []plugins.Plugin{
	news.Plugin{},
	twitter.Plugin{},
	viepublique.Plugin{},
	photos.Plugin{},
	frpolitics.Plugin{},
}
