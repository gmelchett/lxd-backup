// Primitive delta backup service for LXD
package main

import (
	"archive/tar"
	"bufio"
	"crypto/md5"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

type runningState int

const (
	stateRunning runningState = iota
	stateStopped
)

type containerState struct {
	name    string
	state   runningState
	profile string
}

func lxcList(host string) []containerState {

	cmd := exec.Command("lxc", "list", "-c", "nsLP", "-f", "csv")
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Failed to get stdout of 'lxc list'. Error: %v\n", err)
	}

	var csvResult strings.Builder

	reader := bufio.NewReader(stdout)

	go func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			csvResult.WriteString(scanner.Text() + "\n")
		}
	}(reader)

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start 'lxc list'. Error %v\n", err)
	}
	cmd.Wait()

	r := csv.NewReader(strings.NewReader(csvResult.String()))

	containersCsv, err := r.ReadAll()

	if err != nil {
		log.Fatalf("Failed to convert raw CSV to [][]string. Error: %v\n", err)
	}

	containers := make([]containerState, 0, len(containersCsv))

	for i := range containersCsv {

		if containersCsv[i][2] == host || len(host) == 0 {
			var s runningState

			switch containersCsv[i][1] {
			case "STOPPED":
				s = stateStopped
			case "RUNNING":
				s = stateRunning
			default:
				log.Fatalf("Unknown state for %s - %s - Giving up.\n", containersCsv[i][0], containersCsv[i][1])
			}
			containers = append(containers, containerState{name: containersCsv[i][0], state: s, profile: containersCsv[i][3]})
		}
	}

	return containers
}

func lxcStop(name string) {
	cmd := exec.Command("lxc", "stop", name)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to run: lxc stop %s. Error: %v\n", name, err)
	}
}

func lxcStart(name string) {
	cmd := exec.Command("lxc", "start", name)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to run: lxc start %s. Error: %v\n", name, err)
	}
}

func lxcExport(name, to string) {
	cmd := exec.Command("lxc", "export", name, to, "--instance-only", "-q", "--compression", "zstd")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to run: lxc export %s %s --instance-only. Error: %v\n", name, to, err)
	}
}

func fetchFileDataFromTar(fname string) map[string]string {

	f, err := os.Open(fname)

	if err != nil {
		log.Fatalf("Failed to open %s. Error: %v\n", fname, err)
	}
	defer f.Close()

	in, err := zstd.NewReader(f)

	if err != nil {
		log.Fatalf("Failed to read %s as zstd compressed file. Error: %v\n", fname, err)
	}
	defer in.Close()

	fd := make(map[string]string)

	tarreader := tar.NewReader(in)

	for {
		hdr, err := tarreader.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			log.Fatalf("Failed to read content of tarfile: %s. Error: %v\n", fname, err)
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		h := md5.New()
		if size, err := io.Copy(h, tarreader); err != nil {
			log.Fatalf("Failed to io.copy from tar to md5sum. Error: %v\n", err)
		} else if int64(size) != hdr.Size {
			log.Fatalf("Failed to read all data of file %s inside %s. Wanted %d got %d\n", hdr.Name, fname, hdr.Size, size)
		}

		var s strings.Builder
		for _, v := range h.Sum(nil) {
			s.WriteString(fmt.Sprintf("%02x", v))
		}
		fd[hdr.Name] = s.String()
	}

	return fd
}

func createDeltaBackup(src string, filesChanged map[string]bool, filesRemoved []string, dest string) {

	if _, err := os.Stat(dest); err == nil {
		// Do nothing, if destination exists
		return
	}

	fin, err := os.Open(src)

	if err != nil {
		log.Fatalf("Failed to open %s. Error: %v\n", src, err)
	}
	defer fin.Close()

	in, err := zstd.NewReader(fin)

	if err != nil {
		log.Fatalf("Failed to read %s as zstd compressed file. Error: %v\n", src, err)
	}
	defer in.Close()

	tarreader := tar.NewReader(in)

	fout, err := os.OpenFile(dest, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)

	if err != nil {
		log.Fatalf("Failed to create %s. Error: %v\n", dest, err)
	}
	defer fout.Close()

	out, err := zstd.NewWriter(fout)

	if err != nil {
		log.Fatalf("Failed write %s as zstd compressed file. Error: %v\n", dest, err)
	}
	defer out.Close()

	tarwriter := tar.NewWriter(out)
	defer tarwriter.Close()

	for {
		hdr, err := tarreader.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			log.Fatalf("Failed to read content of tarfile: %s. Error: %v\n", src, err)
		}
		if _, present := filesChanged[hdr.Name]; present {

			if err := tarwriter.WriteHeader(hdr); err != nil {
				log.Fatalf("Failed to write tar header: %v\n", err)
			}
			d := make([]byte, hdr.Size)
			if n, err := tarreader.Read(d); err != nil {
				log.Fatalf("Failed to read file in tar: %v\n", err)
			} else if n != len(d) {
				log.Fatalf("tar Input truncated! Wanted %d bytes got %d\n", len(d), n)
			}

			if _, err := tarwriter.Write(d); err != nil {
				log.Fatalf("Failed to write data to file: %v\n", err)
			}
		}
	}

	fr, err := os.OpenFile(dest+".removed", os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to create list of removed files %s. Error: %v\n", dest+".removed", err)
	}
	defer fr.Close()
	for i := range filesRemoved {
		fr.WriteString(filesRemoved[i] + "\n")
	}
}

func writeFileData(out string, fd map[string]string) {

	fdnames := make([]string, 0, len(fd))
	for v := range fd {
		fdnames = append(fdnames, v)
	}
	sort.Strings(fdnames)

	fl := make([][]string, 0, len(fd))
	for i := range fdnames {
		fl = append(fl, []string{fdnames[i], fd[fdnames[i]]})
	}

	f, err := os.OpenFile(out, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to create filedata file %s. Error: %v\n", out, err)
	}
	defer f.Close()

	csvWriter := csv.NewWriter(f)
	if err := csvWriter.WriteAll(fl); err != nil {
		log.Fatalf("Fail to write filedata to csv %s. Error: %v\n", out, err)
	}
}

func loadFileData(fname string) map[string]string {

	f, err := os.Open(fname)
	if err != nil {
		log.Fatalf("Failed to open: %s. Error: %v\n", fname, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	c, err := r.ReadAll()
	if err != nil {
		log.Fatalf("Failed to decode csv in %s. Error: %v\n", fname, err)
	}

	checksums := make(map[string]string)
	for _, l := range c {
		checksums[l[0]] = l[1]
	}
	return checksums
}

func main() {

	backupTarget := ""

	lxdBackupPrefix := filepath.Join(backupTarget, "lxd-backup-")

	now := time.Now()
	_, w := now.ISOWeek()

	quarter := fmt.Sprintf("Q%d%d.tar.zst", now.Year(), now.Month()/4) // Lasts "forever"
	monthDelta := fmt.Sprintf("M%d-delta.tar.zst", now.Month())        // Last a year
	weekDelta := fmt.Sprintf("WN%d-delta.tar.zst", w%4)                // Lasts a month
	dayDelta := fmt.Sprintf("WD%d-delta.tar.zst", now.Weekday())       // Last a week, 0 = Sunday

	for _, c := range lxcList("") {

		if c.name != "backup-test" {
			continue
		}

		fmt.Println(c)

		if c.state == stateRunning {
			lxcStop(c.name)
		}

		var exportName string
		doDelta := false

		qBackup := lxdBackupPrefix + c.name + quarter
		if _, err := os.Stat(qBackup); errors.Is(err, os.ErrNotExist) {
			exportName = qBackup
		} else {
			exportName = filepath.Join(backupTarget, fmt.Sprintf("lxd-temporary-backup-%d.tar.zstd", time.Now().UnixNano()))
			doDelta = true
		}

		lxcExport(c.name, exportName)

		if c.state == stateRunning {
			lxcStart(c.name)
		}

		sums := fetchFileDataFromTar(exportName) // calculate md5sums

		if !doDelta {
			// Save md5sums for quarterly
			writeFileData(exportName+".md5sum", sums)
			continue
		}

		quarterSums := loadFileData(qBackup + ".md5sum")

		filesChangedAdded := make(map[string]bool)
		var filesRemoved []string

		// Look for files changed or delete compared with quarter
		for fname, md5sumOld := range quarterSums {
			if md5sumCurr, present := sums[fname]; present {
				if md5sumCurr != md5sumOld {
					filesChangedAdded[fname] = true
				}
			} else {
				filesRemoved = append(filesRemoved, fname)
			}
		}

		// New files compared with quarter?
		for fname := range sums {
			if _, present := quarterSums[fname]; !present {
				filesChangedAdded[fname] = true
			}
		}

		if len(filesChangedAdded) == 0 && len(filesRemoved) == 0 {
			ioutil.WriteFile(lxdBackupPrefix+c.name+".log", []byte(fmt.Sprintf("%s: No changes\n", now.String())), 0644)
			continue
		}

		// Create delta(s)
		if now.Day() == 1 {
			os.Remove(lxdBackupPrefix + c.name + monthDelta)
		}
		if now.Weekday() == 1 { // monday
			os.Remove(lxdBackupPrefix + c.name + weekDelta)
		}
		os.Remove(lxdBackupPrefix + c.name + dayDelta)

		// FIXME: There is no delta of delta, month, week and day will sometimes contain the same data
		createDeltaBackup(exportName, filesChangedAdded, filesRemoved, monthDelta)
		createDeltaBackup(exportName, filesChangedAdded, filesRemoved, weekDelta)
		createDeltaBackup(exportName, filesChangedAdded, filesRemoved, dayDelta)

		status := fmt.Sprintf("%s: %d files changed/added, %d removed.\n", now.String(), len(filesChangedAdded), len(filesRemoved))
		if err := ioutil.WriteFile(lxdBackupPrefix+c.name+".log", []byte(status), 0644); err != nil {
			log.Fatalf("Failed to write log for %s: %v\n", c.name, err)
		}
		os.Remove(exportName)
	}
}
