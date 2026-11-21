package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/matcornic/subify/common/utils"
	"github.com/matcornic/subify/subtitles"
)

var overwriteExisting bool

// dlDirCmd represents the dl-dir command, which downloads subtitles for every
// supported video in a directory
var dlDirCmd = &cobra.Command{
	Use:     "dl-dir <directory-path>",
	Aliases: []string{"dldir", "batch"},
	Short:   "Download subtitles for every video of a directory - 'subify dl-dir --help'",
	Long: `Download subtitles for every video (movie or TV Shows) contained in a directory.
Give the path of the directory as first parameter. Videos are processed in file
name order, with the same language and API priority as the 'dl' command. A
failure on one video does not interrupt the other ones, and a per-video report
is printed at the end.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			utils.Exit("Directory needed. See usage : 'subify help' or 'subify dl-dir --help'")
		}
		dirPath := args[0]

		apis := strings.Split(viper.GetString("download.apis"), ",")
		languages := strings.Split(viper.GetString("download.languages"), ",")
		options := []func(*subtitles.DownloadOptions){}
		if langInFileName {
			options = append(options, subtitles.WithLangInFileName())
		}
		if notify {
			options = append(options, subtitles.WithOSNotification())
		}
		if overwriteExisting {
			options = append(options, subtitles.WithOverwrite())
		}

		report, err := subtitles.DownloadDir(dirPath, apis, languages, options...)
		if err != nil {
			utils.ExitPrintError(err, "Sadly, we could not scan the directory %s", dirPath)
		}

		printBatchReport(report)

		if report.HasFailure() {
			utils.Exit("Batch download finished with %d failed/conflicting video(s) out of %d. See the report above.",
				report.Count(subtitles.StatusFailed)+report.Count(subtitles.StatusConflict),
				len(report.Videos))
		}
	},
}

func init() {
	dlDirCmd.Flags().StringP("languages", "l", "en", "Languages of the subtitle separated by a comma (First to match is downloaded). Available languages at 'subify list languages'")
	dlDirCmd.Flags().StringP("apis", "a", "OpenSubtitles,Addic7ed", "Overwrite default searching APIs behavior, hence the subtitles are downloaded. Available APIs at 'subify list apis'")
	dlDirCmd.Flags().BoolVarP(&notify, "notify", "n", false, "Display desktop notification")
	dlDirCmd.Flags().BoolVar(&langInFileName, "lang-in-filename", true, "Language is in the filename (e.g. if enabled: 'xxx.English.srt' for English), else 'xxx.srt'")
	dlDirCmd.Flags().BoolVar(&overwriteExisting, "overwrite", false, "Overwrite a subtitle that already exists next to a video (default: skip the video)")
	_ = viper.BindPFlag("download.languages", dlDirCmd.Flags().Lookup("languages"))
	_ = viper.BindPFlag("download.apis", dlDirCmd.Flags().Lookup("apis"))

	RootCmd.AddCommand(dlDirCmd)
}

// printBatchReport prints the per-video outcome of a batch download, followed
// by the ignored entries and a summary
func printBatchReport(report *subtitles.BatchReport) {
	if len(report.Videos) == 0 && len(report.Ignored) == 0 {
		fmt.Printf("Directory %s is empty, no video to process.\n", report.Directory)
		return
	}

	if len(report.Videos) > 0 {
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Video", "Status", "Language", "API", "Subtitle", "Details"})
		for _, v := range report.Videos {
			table.Append([]string{
				v.Video,
				v.Status,
				dashIfEmpty(v.Language),
				dashIfEmpty(v.API),
				dashIfEmpty(v.Subtitle),
				v.Error,
			})
		}
		table.SetAutoWrapText(false)
		table.SetColWidth(50)
		table.Render()
	} else {
		fmt.Printf("No supported video found in %s.\n", report.Directory)
	}

	if len(report.Ignored) > 0 {
		fmt.Println()
		fmt.Println("Ignored entries:")
		ignoredTable := tablewriter.NewWriter(os.Stdout)
		ignoredTable.SetHeader([]string{"Entry", "Reason"})
		for _, i := range report.Ignored {
			ignoredTable.Append([]string{i.Path, i.Reason})
		}
		ignoredTable.SetAutoWrapText(false)
		ignoredTable.SetColWidth(50)
		ignoredTable.Render()
	}

	fmt.Println()
	fmt.Printf("Summary: %d downloaded, %d skipped, %d failed, %d conflict(s)\n",
		report.Count(subtitles.StatusDownloaded),
		report.Count(subtitles.StatusSkipped),
		report.Count(subtitles.StatusFailed),
		report.Count(subtitles.StatusConflict))
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
