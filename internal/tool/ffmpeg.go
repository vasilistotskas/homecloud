package tool

// FFmpegFile returns the ffmpeg or ffprobe arguments that open path as an
// input: as a local file only. ffmpeg otherwise accepts any URL it has a
// protocol for, and a full build has many (http, tcp, ftp, concat, subfile):
// a path is taken as one, and a crafted file (a playlist) can name others.
// The whitelist holds for everything the input opens.
func FFmpegFile(path string) []string {
	return []string{"-protocol_whitelist", "file", "-i", "file:" + path}
}

// FFmpegStdin returns the arguments that read one input of format from
// standard input, and nothing else.
func FFmpegStdin(format string) []string {
	return []string{"-protocol_whitelist", "pipe", "-f", format, "-i", "pipe:0"}
}
