package subtitles

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCall records one invocation of a fake client
type fakeCall struct {
	client string
	video  string
	langID string
}

// fakeClient is a deterministic, offline implementation of Client (and
// LanguageTokenizer) used to verify batch behavior without any network access.
type fakeClient struct {
	name       string
	calls      *[]fakeCall
	mu         *sync.Mutex
	fail       func(videoPath string, lang Language) bool
	panicOn    func(videoPath string, lang Language) bool
	tokenFor   func(lang Language) (string, bool)
	writtenTag string // content written inside the subtitle file
}

func (f *fakeClient) GetName() string      { return f.name }
func (f *fakeClient) GetAliases() []string { return []string{f.name} }

func (f *fakeClient) Upload(string, Language, string) error { return nil }

func (f *fakeClient) LanguageToken(lang Language) (string, bool) {
	if f.tokenFor != nil {
		return f.tokenFor(lang)
	}
	return "tok-" + lang.ID, true
}

func (f *fakeClient) Download(videoPath string, lang Language, options ...func(*DownloadOptions)) (string, error) {
	f.mu.Lock()
	*f.calls = append(*f.calls, fakeCall{client: f.name, video: videoPath, langID: lang.ID})
	f.mu.Unlock()

	if f.panicOn != nil && f.panicOn(videoPath, lang) {
		panic("boom from " + f.name)
	}
	if f.fail != nil && f.fail(videoPath, lang) {
		return "", assert.AnError
	}

	opts := &DownloadOptions{}
	for _, o := range options {
		o(opts)
	}
	token, _ := f.LanguageToken(lang)
	subtitlePath := SubtitlePath(videoPath, token, opts.langInFileName)
	if err := os.WriteFile(subtitlePath, []byte(f.writtenTag), 0644); err != nil {
		return "", err
	}
	return subtitlePath, nil
}

func newFakeClients(calls *[]fakeCall, mu *sync.Mutex, names ...string) Clients {
	clients := Clients{}
	for _, n := range names {
		clients = append(clients, &fakeClient{name: n, calls: calls, mu: mu, writtenTag: n})
	}
	return clients
}

func mustLanguages(ids ...string) Langs {
	langs := Langs{}
	for _, id := range ids {
		lang := Languages.GetLanguage(id)
		if lang == nil {
			panic("unknown test language " + id)
		}
		langs = append(langs, *lang)
	}
	return langs
}

func writeEmptyFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))
}

func videoBase(videoPath string) string {
	base := filepath.Base(videoPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func TestScanVideosOrdersByNameAndReportsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "z-last.mkv"))
	writeEmptyFile(t, filepath.Join(dir, "a-first.mp4"))
	writeEmptyFile(t, filepath.Join(dir, "m-middle.MKV")) // uppercase extension, still supported
	writeEmptyFile(t, filepath.Join(dir, "notes.txt"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "Season 2"), 0755))

	videos, ignored, err := ScanVideos(dir)
	require.NoError(t, err)

	require.Len(t, videos, 3)
	assert.Equal(t, filepath.Join(dir, "a-first.mp4"), videos[0])
	assert.Equal(t, filepath.Join(dir, "m-middle.MKV"), videos[1])
	assert.Equal(t, filepath.Join(dir, "z-last.mkv"), videos[2])

	ignoredPaths := map[string]string{}
	for _, i := range ignored {
		ignoredPaths[i.Path] = i.Reason
	}
	assert.Contains(t, ignoredPaths, filepath.Join(dir, "notes.txt"))
	assert.Contains(t, ignoredPaths[filepath.Join(dir, "notes.txt")], "unsupported extension")
	assert.Contains(t, ignoredPaths, filepath.Join(dir, "Season 2"))
	assert.Contains(t, ignoredPaths[filepath.Join(dir, "Season 2")], "not recursive")
}

func TestScanVideosEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	videos, ignored, err := ScanVideos(dir)
	require.NoError(t, err)
	assert.Empty(t, videos)
	assert.Empty(t, ignored)
}

func TestDownloadDirProcessesInStableOrderAndFillsReport(t *testing.T) {
	dir := t.TempDir()
	names := []string{"show.S01E03.mp4", "show.S01E01.mkv", "show.S01E02.avi"}
	for _, n := range names {
		writeEmptyFile(t, filepath.Join(dir, n))
	}
	sort.Strings(names) // expected processing order

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA", "apiB")
	langs := mustLanguages("en")

	report := downloadDir(dir, clients, langs, WithLangInFileName())

	require.Len(t, report.Videos, 3)
	assert.Empty(t, report.Ignored)

	// Every video succeeds with the first API, English language
	for i, n := range names {
		video := filepath.Join(dir, n)
		r := report.Videos[i]
		assert.Equal(t, video, r.Video, "videos must be processed in file name order")
		assert.Equal(t, StatusDownloaded, r.Status)
		assert.Equal(t, "English", r.Language)
		assert.Equal(t, "apiA", r.API)
		expected := SubtitlePath(video, "tok-eng", true)
		assert.Equal(t, expected, r.Subtitle)
		_, err := os.Stat(expected)
		assert.NoError(t, err, "subtitle file must exist")
	}

	// Calls follow the same order, and only the first API is consulted
	require.Len(t, calls, 3)
	for i, n := range names {
		assert.Equal(t, filepath.Join(dir, n), calls[i].video)
		assert.Equal(t, "apiA", calls[i].client)
		assert.Equal(t, "eng", calls[i].langID)
	}
	assert.False(t, report.HasFailure())
	assert.Equal(t, 3, report.Count(StatusDownloaded))
}

func TestDownloadDirFallsBackToNextAPI(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "show.mp4"))

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA", "apiB")
	clients[0].(*fakeClient).fail = func(videoPath string, lang Language) bool { return true }

	report := downloadDir(dir, clients, mustLanguages("en"), WithLangInFileName())

	require.Len(t, report.Videos, 1)
	r := report.Videos[0]
	assert.Equal(t, StatusDownloaded, r.Status)
	assert.Equal(t, "apiB", r.API, "second API must be used after the first one fails")
	assert.Equal(t, "English", r.Language)
	require.Len(t, calls, 2)
	assert.Equal(t, "apiA", calls[0].client)
	assert.Equal(t, "apiB", calls[1].client)
}

func TestDownloadDirFallsBackToNextLanguage(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "show.mp4"))

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA", "apiB")
	// English never available, French is
	for _, c := range clients {
		c.(*fakeClient).fail = func(_ string, lang Language) bool { return lang.ID == "eng" }
	}

	report := downloadDir(dir, clients, mustLanguages("en", "fr"), WithLangInFileName())

	r := report.Videos[0]
	assert.Equal(t, StatusDownloaded, r.Status)
	assert.Equal(t, "French", r.Language)
	// English must have been tried on both APIs before French
	seen := []string{}
	for _, c := range calls {
		seen = append(seen, c.client+"/"+c.langID)
	}
	assert.Equal(t, []string{"apiA/eng", "apiB/eng", "apiA/fre"}, seen)
	assert.Contains(t, r.Subtitle, "tok-fre.srt")
}

func TestDownloadDirPartialFailureDoesNotInterruptOthers(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "a-good.mp4"))
	writeEmptyFile(t, filepath.Join(dir, "b-broken.mkv"))
	writeEmptyFile(t, filepath.Join(dir, "c-good.avi"))

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA", "apiB")
	for _, c := range clients {
		c.(*fakeClient).fail = func(videoPath string, _ Language) bool {
			return videoBase(videoPath) == "b-broken"
		}
	}

	report := downloadDir(dir, clients, mustLanguages("en"), WithLangInFileName())

	require.Len(t, report.Videos, 3)
	assert.Equal(t, StatusDownloaded, report.Videos[0].Status)
	assert.Equal(t, StatusFailed, report.Videos[1].Status)
	assert.NotEmpty(t, report.Videos[1].Error)
	assert.Empty(t, report.Videos[1].Subtitle)
	assert.Equal(t, StatusDownloaded, report.Videos[2].Status, "later videos must still be processed")
	assert.True(t, report.HasFailure())
	assert.Equal(t, 1, report.Count(StatusFailed))
	assert.Equal(t, 2, report.Count(StatusDownloaded))

	// No subtitle file for the failed video
	_, err := os.Stat(filepath.Join(dir, "b-broken.tok-eng.srt"))
	assert.True(t, os.IsNotExist(err))
}

func TestDownloadDirSkipsExistingSubtitle(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "show.mp4")
	writeEmptyFile(t, video)
	existing := filepath.Join(dir, "show.tok-eng.srt")
	writeEmptyFile(t, existing)

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA", "apiB")

	// Default: skip
	report := downloadDir(dir, clients, mustLanguages("en", "fr"), WithLangInFileName())
	r := report.Videos[0]
	assert.Equal(t, StatusSkipped, r.Status)
	assert.Equal(t, existing, r.Subtitle)
	assert.Empty(t, calls, "no API must be called when the subtitle already exists")

	// Overwrite: subtitle is downloaded again
	report = downloadDir(dir, clients, mustLanguages("en", "fr"), WithLangInFileName(), WithOverwrite())
	r = report.Videos[0]
	assert.Equal(t, StatusDownloaded, r.Status)
	assert.Equal(t, "apiA", r.API)
	require.Len(t, calls, 1)
	content, err := os.ReadFile(existing)
	require.NoError(t, err)
	assert.Equal(t, "apiA", string(content))
}

func TestDownloadDirSkipsExistingSubtitleWithoutLangInFilename(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "show.mp4")
	writeEmptyFile(t, video)
	existing := filepath.Join(dir, "show.srt")
	writeEmptyFile(t, existing)

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA")

	report := downloadDir(dir, clients, mustLanguages("en")) // no WithLangInFileName
	r := report.Videos[0]
	assert.Equal(t, StatusSkipped, r.Status)
	assert.Equal(t, existing, r.Subtitle)
	assert.Empty(t, calls)
}

func TestDownloadDirDuplicateBaseNamesConflict(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "show.mp4"))
	writeEmptyFile(t, filepath.Join(dir, "show.mkv")) // same base name, would write the same .srt

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA")

	report := downloadDir(dir, clients, mustLanguages("en"), WithLangInFileName())

	require.Len(t, report.Videos, 2)
	// Conflict results must stay in file name order, not map-iteration order
	assert.Equal(t, filepath.Join(dir, "show.mkv"), report.Videos[0].Video)
	assert.Equal(t, filepath.Join(dir, "show.mp4"), report.Videos[1].Video)
	for _, r := range report.Videos {
		assert.Equal(t, StatusConflict, r.Status)
		assert.Contains(t, r.Error, "duplicate video name")
	}
	assert.Empty(t, calls, "conflicting videos must not be downloaded")
	assert.True(t, report.HasFailure())
}

func TestDownloadDirNoSupportedVideo(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "readme.txt"))

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA")

	report := downloadDir(dir, clients, mustLanguages("en"), WithLangInFileName())
	assert.Empty(t, report.Videos)
	require.Len(t, report.Ignored, 1)
	assert.Contains(t, report.Ignored[0].Reason, "unsupported extension")
	assert.False(t, report.HasFailure())
}

func TestDownloadDirPanickingClientIsIsolated(t *testing.T) {
	dir := t.TempDir()
	writeEmptyFile(t, filepath.Join(dir, "a-panic.mp4"))
	writeEmptyFile(t, filepath.Join(dir, "b-ok.mkv"))

	var mu sync.Mutex
	calls := []fakeCall{}
	clients := newFakeClients(&calls, &mu, "apiA")
	clients[0].(*fakeClient).panicOn = func(videoPath string, _ Language) bool {
		return videoBase(videoPath) == "a-panic"
	}

	report := downloadDir(dir, clients, mustLanguages("en"), WithLangInFileName())

	require.Len(t, report.Videos, 2)
	assert.Equal(t, StatusFailed, report.Videos[0].Status)
	assert.Contains(t, report.Videos[0].Error, "boom")
	assert.Equal(t, StatusDownloaded, report.Videos[1].Status, "a panic must not interrupt the batch")
}

func TestSubtitlePathMatchesSingleFileNaming(t *testing.T) {
	// With language in file name
	assert.Equal(t, "/tmp/show.eng.srt", SubtitlePath("/tmp/show.mp4", "eng", true))
	// Without language in file name
	assert.Equal(t, "/tmp/show.srt", SubtitlePath("/tmp/show.mp4", "eng", false))
}

func TestDownloadDirRejectsNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.mp4")
	writeEmptyFile(t, file)

	report, err := DownloadDir(file, []string{"apiA"}, []string{"en"})
	assert.Error(t, err)
	assert.Nil(t, report)
}

func TestDownloadDirRejectsUnknownLanguages(t *testing.T) {
	dir := t.TempDir()
	report, err := DownloadDir(dir, []string{"apiA"}, []string{"klingon-ish"})
	assert.Error(t, err)
	assert.Nil(t, report)
}

func TestDownloadDirEmptyDirectoryReport(t *testing.T) {
	dir := t.TempDir()
	report, err := DownloadDir(dir, []string{"apiA"}, []string{"en"})
	require.NoError(t, err)
	assert.Empty(t, report.Videos)
	assert.Empty(t, report.Ignored)
	assert.False(t, report.HasFailure())
}
