package subtitles

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	logger "github.com/spf13/jwalterweatherman"

	"github.com/matcornic/subify/notif"
)

// Video download statuses, reported per video in a batch download
const (
	// StatusDownloaded means a subtitle was found and written to disk
	StatusDownloaded = "downloaded"
	// StatusSkipped means a subtitle already existed next to the video and overwrite was disabled
	StatusSkipped = "skipped"
	// StatusFailed means no subtitle could be downloaded, even after trying every language and API
	StatusFailed = "failed"
	// StatusConflict means several videos resolve to the same subtitle output path
	StatusConflict = "conflict"
)

// SupportedVideoExtensions is the set of file extensions Subify looks for
// when scanning a directory. Matching is case-insensitive.
var SupportedVideoExtensions = map[string]bool{
	".avi":  true,
	".m4v":  true,
	".mkv":  true,
	".mov":  true,
	".mp4":  true,
	".wmv":  true,
	".flv":  true,
	".webm": true,
	".mpg":  true,
	".mpeg": true,
	".ts":   true,
}

type DownloadOptions struct {
	langInFileName bool
	notify         bool
	// overwrite replaces an existing subtitle file instead of skipping the video
	overwrite bool
}

func WithLangInFileName() func(*DownloadOptions) {
	return func(s *DownloadOptions) {
		s.langInFileName = true
	}
}

func WithOSNotification() func(*DownloadOptions) {
	return func(s *DownloadOptions) {
		s.notify = true
	}
}

// WithOverwrite makes a download replace an existing subtitle file instead
// of being skipped. Only used by batch downloads.
func WithOverwrite() func(*DownloadOptions) {
	return func(s *DownloadOptions) {
		s.overwrite = true
	}
}

// Client defines the interface to get subtitles from API
type Client interface {
	Download(videoPath string, language Language, options ...func(*DownloadOptions)) (subtitlePath string, err error)
	Upload(subtitlePath string, language Language, videoPath string) error
	GetName() string
	GetAliases() []string
}

// LanguageTokenizer is implemented by clients whose subtitle file names carry
// a language token specific to the API (e.g. "eng" for OpenSubtitles, "English"
// for Addic7ed). It is used to locate subtitles already downloaded next to a
// video, so batch mode can skip them.
type LanguageTokenizer interface {
	LanguageToken(language Language) (string, bool)
}

// SubtitlePath computes the path of the subtitle file for the given video,
// using the exact same naming rule as the single-file download:
// <video-base>.<token>.srt when the language is in the file name, <video-base>.srt otherwise.
// token is only used when langInFileName is true.
func SubtitlePath(videoPath string, token string, langInFileName bool) string {
	basePath := strings.TrimSuffix(videoPath, path.Ext(videoPath))
	if langInFileName {
		return basePath + "." + token + ".srt"
	}
	return basePath + ".srt"
}

// Clients is a slice of Client
type Clients []Client

// DefaultAPIs represents the available APIs
// Is also used as the default
var DefaultAPIs = Clients{
	OpenSubtitles(),
	Addic7ed(),
}

// InitAPIs sets the order of APIs search from apiAliases
// If alias does not exists, it is not included
func InitAPIs(apiAliases []string) (apis Clients) {
	for _, alias := range apiAliases {
		for _, availableAPI := range DefaultAPIs {
			for _, availableAliases := range availableAPI.GetAliases() {
				if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(availableAliases)) {
					apis = append(apis, availableAPI)
				}
			}
		}
	}
	return
}

// Print prints the clients as nice table
func (c Clients) Print() {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Name", "Aliases"})
	for _, l := range c {
		values := []string{
			l.GetName(),                        // Name
			strings.Join(l.GetAliases(), ", "), // Aliases
		}
		table.Append(values)
	}
	table.SetAutoWrapText(false)
	table.SetColWidth(50)
	table.SetRowLine(true)
	table.Render() // Send output
}

// String prints a nice representation of clients
func (c Clients) String() (s string) {
	for i, v := range c {
		s = s + v.GetName()
		if (i + 1) < len(c) {
			s = s + ", "
		}
	}
	return
}

// Download the subtitle from the video identified by its path
func Download(videoPath string, apiAliases []string, languages []string, downloadOptions ...func(*DownloadOptions)) error {
	// Gets APIs
	a := InitAPIs(apiAliases)
	if len(a) == 0 {
		a = DefaultAPIs
		logger.WARN.Println("No API has been recognized by Subify. Using default:", DefaultAPIs)
	} else if len(apiAliases) != len(a) {
		logger.WARN.Println("Some languages are not recognized. Given:", apiAliases, "Found:", a)
	}

	// Check languages
	l := Languages.GetLanguages(languages)
	if len(l) == 0 {
		logger.ERROR.Println("Languages", languages, "are not available. Pick one ore more from the table below :")
		Languages.Print(false)
		return fmt.Errorf("No languages is available for given languages : %v", languages)
	} else if len(languages) != len(l) {
		logger.WARN.Println("Some languages are not recognized. Given:", languages, "Found:", l.GetDescriptions())
	}

	_, _, _, err := downloadOne(videoPath, a, l, downloadOptions...)
	return err
}

// downloadOne downloads one subtitle for one video, reusing the given clients
// and languages in priority order (first language wins, first API that finds a
// subtitle wins). Returns the path of the downloaded subtitle, the language and
// the API that succeeded.
func downloadOne(videoPath string, clients Clients, languages Langs, downloadOptions ...func(*DownloadOptions)) (subtitlePath string, lang Language, api Client, err error) {
	options := &DownloadOptions{}
	for _, o := range downloadOptions {
		o(options)
	}

	// Run through languages
	for i, l := range languages {
		lang = l
		// Run through different APIs to get the subtitle. Stops when found
		logger.INFO.Println("===> ("+strconv.Itoa(i+1)+") Searching subtitles for", l.Description, "language")
		for j, a := range clients {
			api = a
			logger.INFO.Println("=> (" + strconv.Itoa(i+1) + "." + strconv.Itoa(j+1) + ") Downloading subtitle with " + a.GetName() + "...")
			subtitlePath, err = a.Download(videoPath, l, downloadOptions...)
			if err == nil {
				if options.notify {
					notif.SendSubtitleDownloadSuccess(a.GetName())
				}
				logger.INFO.Println(l.Description, "subtitle found and saved to ", subtitlePath)
				return subtitlePath, l, a, nil
			}
			logger.INFO.Println("Subtitle not found because :", err.Error())
			if (j + 1) < len(clients) {
				logger.INFO.Println("Trying with another API...")
			}
		}
		logger.INFO.Println("=> No subtitle found in", l.Description, "language.")
		if (i + 1) < len(languages) {
			logger.INFO.Println("Trying with another language...")
		}
	}

	if options.notify {
		notif.SendSubtitleCouldNotBeDownloaded(clients.String())
	}
	return "", Language{}, nil, fmt.Errorf("No %v subtitle found, even after searching in all APIs (%v)", strings.Join(languages.GetDescriptions(), ", nor "), clients.String())
}

// IgnoredFile explains why an entry of a scanned directory was not processed
type IgnoredFile struct {
	Path   string
	Reason string
}

// ScanVideos lists the video files of a directory, in stable file name order
// (case-sensitive lexicographic order of the base name). Entries that are not
// supported videos (directories, unsupported extensions...) are returned with
// an explanation instead of being silently dropped. The scan is not recursive.
func ScanVideos(dir string) (videos []string, ignored []IgnoredFile, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	// os.ReadDir already sorts by name, but sorting again makes the ordering
	// guarantee explicit and independent from the underlying implementation.
	sort.Strings(names)

	for _, name := range names {
		entryPath := filepath.Join(dir, name)
		info, infoErr := os.Stat(entryPath)
		switch {
		case infoErr != nil:
			ignored = append(ignored, IgnoredFile{Path: entryPath, Reason: "cannot access file: " + infoErr.Error()})
		case info.IsDir():
			ignored = append(ignored, IgnoredFile{Path: entryPath, Reason: "directory (batch download is not recursive)"})
		case !SupportedVideoExtensions[strings.ToLower(filepath.Ext(name))]:
			ignored = append(ignored, IgnoredFile{Path: entryPath, Reason: "unsupported extension " + filepath.Ext(name)})
		default:
			videos = append(videos, entryPath)
		}
	}
	return videos, ignored, nil
}

// VideoResult is the outcome of the batch download for one video
type VideoResult struct {
	Video    string // Absolute or given path of the video
	Status   string // One of the Status* constants
	Language string // Description of the language of the downloaded/skipped subtitle ("" if none)
	API      string // Name of the API that found the subtitle ("" if none)
	Subtitle string // Path of the downloaded or pre-existing subtitle ("" if none)
	Error    string // Error message when Status is failed/conflict ("" otherwise)
}

// BatchReport aggregates the result of a directory batch download
type BatchReport struct {
	Directory string
	Videos    []VideoResult
	Ignored   []IgnoredFile
}

// Count returns the number of videos in the given status
func (r *BatchReport) Count(status string) int {
	count := 0
	for _, v := range r.Videos {
		if v.Status == status {
			count++
		}
	}
	return count
}

// HasFailure reports whether at least one video was not processed successfully
func (r *BatchReport) HasFailure() bool {
	for _, v := range r.Videos {
		if v.Status == StatusFailed || v.Status == StatusConflict {
			return true
		}
	}
	return false
}

// existingSubtitle returns the path of a subtitle file that already exists next
// to the video for one of the requested languages/APIs, and "" otherwise.
// Candidates follow the exact naming rule of SubtitlePath.
func existingSubtitle(videoPath string, clients Clients, languages Langs, options *DownloadOptions) string {
	if !options.langInFileName {
		candidate := SubtitlePath(videoPath, "", false)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}
	for _, l := range languages {
		for _, c := range clients {
			tokenizer, ok := c.(LanguageTokenizer)
			if !ok {
				continue
			}
			token, ok := tokenizer.LanguageToken(l)
			if !ok {
				continue
			}
			candidate := SubtitlePath(videoPath, token, true)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// downloadDir is the testable batch implementation: the clients are injected so
// unit tests can use fake APIs instead of hitting the network. Every video is
// processed independently: a failure (including a panic in a client) never
// interrupts the remaining videos.
func downloadDir(dir string, clients Clients, languages Langs, downloadOptions ...func(*DownloadOptions)) *BatchReport {
	report := &BatchReport{Directory: dir}

	videos, ignored, err := ScanVideos(dir)
	report.Ignored = ignored
	if err != nil {
		report.Videos = append(report.Videos, VideoResult{
			Video:  dir,
			Status: StatusFailed,
			Error:  err.Error(),
		})
		return report
	}

	options := &DownloadOptions{}
	for _, o := range downloadOptions {
		o(options)
	}

	// Detect videos sharing the same base name: their subtitle output paths
	// collide, so they are reported as conflicts instead of overwriting each other.
	baseCounts := map[string]int{}
	for _, video := range videos {
		base := strings.TrimSuffix(video, path.Ext(video))
		baseCounts[base]++
	}

	// claimedPaths guards against output path collisions at runtime, beyond the
	// base-name duplicates detected above.
	claimedPaths := map[string]string{}

	for _, video := range videos {
		result := VideoResult{Video: video}

		if base := strings.TrimSuffix(video, path.Ext(video)); baseCounts[base] > 1 {
			result.Status = StatusConflict
			result.Error = fmt.Sprintf("duplicate video name: %d videos resolve to the same subtitle path %s.srt", baseCounts[base], base)
			report.Videos = append(report.Videos, result)
			continue
		}

		if !options.overwrite {
			if existing := existingSubtitle(video, clients, languages, options); existing != "" {
				result.Status = StatusSkipped
				result.Subtitle = existing
				report.Videos = append(report.Videos, result)
				logger.INFO.Println("Subtitle already exists, skipping", video, "->", existing)
				continue
			}
		}

		// A panicking client must not interrupt the remaining videos
		subtitlePath, usedLang, usedAPI, dlErr := func() (p string, l Language, c Client, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.ERROR.Println("Unexpected failure while downloading subtitle for", video, ":", rec)
					err = fmt.Errorf("unexpected failure: %v", rec)
				}
			}()
			return downloadOne(video, clients, languages, downloadOptions...)
		}()

		if dlErr != nil {
			result.Status = StatusFailed
			result.Error = dlErr.Error()
			report.Videos = append(report.Videos, result)
			continue
		}

		if otherVideo, taken := claimedPaths[subtitlePath]; taken {
			result.Status = StatusConflict
			result.Error = fmt.Sprintf("subtitle path %s is also produced by %s", subtitlePath, otherVideo)
			report.Videos = append(report.Videos, result)
			continue
		}
		claimedPaths[subtitlePath] = video

		result.Status = StatusDownloaded
		result.Language = usedLang.Description
		result.API = usedAPI.GetName()
		result.Subtitle = subtitlePath
		report.Videos = append(report.Videos, result)
	}

	return report
}

// DownloadDir downloads subtitles for every supported video in a directory,
// reusing the language and API priority of the single-file Download function.
// The scan is not recursive and videos are processed in file name order. A
// failure on one video never interrupts the other ones; the full outcome is
// returned in the BatchReport.
func DownloadDir(dir string, apiAliases []string, languageAliases []string, downloadOptions ...func(*DownloadOptions)) (*BatchReport, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	// Gets APIs
	clients := InitAPIs(apiAliases)
	if len(clients) == 0 {
		clients = DefaultAPIs
		logger.WARN.Println("No API has been recognized by Subify. Using default:", DefaultAPIs)
	} else if len(apiAliases) != len(clients) {
		logger.WARN.Println("Some APIs are not recognized. Given:", apiAliases, "Found:", clients)
	}

	// Check languages
	languages := Languages.GetLanguages(languageAliases)
	if len(languages) == 0 {
		logger.ERROR.Println("Languages", languageAliases, "are not available. Pick one or more from the table below :")
		Languages.Print(false)
		return nil, fmt.Errorf("no language is available for given languages : %v", languageAliases)
	} else if len(languageAliases) != len(languages) {
		logger.WARN.Println("Some languages are not recognized. Given:", languageAliases, "Found:", languages.GetDescriptions())
	}

	return downloadDir(dir, clients, languages, downloadOptions...), nil
}
