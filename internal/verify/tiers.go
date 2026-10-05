package verify

import (
	"path/filepath"
	"strings"
)

// isoBMFFVideo lists video containers in the ISO-BMFF / QuickTime family, the
// only formats tier 2's box walk understands.
var isoBMFFVideo = map[string]bool{".mov": true, ".mp4": true, ".m4v": true}

// RequiredTiers reports which tiers beyond content (always required) a file
// needs. Every video needs the ffprobe tier; only ISO-BMFF / QuickTime
// containers also get the box-structure tier (an animated GIF, which kei
// classifies as video, is probed but not box-walked).
func RequiredTiers(mediaType, filename string) (structure, probe bool) {
	if mediaType != "video" && mediaType != "live_photo_video" {
		return false, false
	}
	return isoBMFFVideo[strings.ToLower(filepath.Ext(filename))], true
}
