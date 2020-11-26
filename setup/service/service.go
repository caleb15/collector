package service

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	s "github.com/pganalyze/collector/setup/state"
)

func RestartPostgres(state *s.SetupState) error {
	usePgCtl := os.Getenv("PGA_SETUP_USE_PG_CTL")

	if usePgCtl != "" {
		return restartPostgresPgCtl(state)
	} else {
		return restartPostgresSystemd()
	}
}

func restartPostgresSystemd() error {
	cmd := exec.Command("systemctl", "restart", "postgresql")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var errInfo = err.Error()
		if len(out) > 0 {
			errInfo += "; " + string(out)
		}
		return fmt.Errorf("failed to restart: %s", errInfo)
	}
	return nil
}

func restartPostgresPgCtl(state *s.SetupState) error {
	row, err := state.QueryRunner.QueryRow("SHOW data_directory")
	if err != nil {
		return err
	}
	dataDir := row.GetString(0)
	dataDirInfo, err := os.Stat(dataDir)

	var uid uint32
	var gid uint32
	if stat, ok := dataDirInfo.Sys().(*syscall.Stat_t); ok {
		uid = stat.Uid
		gid = stat.Gid
	} else {
		return errors.New("could not determine data directory ownership")
	}
	datDirOwner, err := user.LookupId(fmt.Sprintf("%d", uid))
	if err != nil {
		return err
	}
	gids, err := datDirOwner.GroupIds()
	if err != nil {
		return fmt.Errorf("could not determine data directory ownership: %s", err)
	}
	var numGids []uint32
	for _, gid := range gids {
		numGid, err := strconv.ParseUint(gid, 10, 32)
		if err != nil {
			return fmt.Errorf("could not determine data directory ownership: user group %s could not be parsed: %s", gid, err)
		}
		numGids = append(numGids, uint32(numGid))
	}

	pgCtlPath, err := getPgCtlLocation()
	cmd := exec.Command(pgCtlPath, "--pgdata", dataDir, "--wait", "--mode", "fast", "restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uid,
		Gid:    gid,
		Groups: numGids,
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		var errInfo = err.Error()
		if len(out) > 0 {
			errInfo += "; " + string(out)
		}
		return fmt.Errorf("failed to restart: %s", errInfo)
	}
	return nil
}

func getPgCtlLocation() (string, error) {
	_, err := exec.Command("pg_ctl", "--help").CombinedOutput()
	if err == nil {
		// it's in PATH, no need to look for it
		return "pg_ctl", nil
	}
	cmd := exec.Command("pg_config")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, _ := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	err = cmd.Start()
	if err != nil {
		return "", err
	}

	stdoutBytes, err := ioutil.ReadAll(stdout)
	if err != nil {
		return "", err
	}
	stderrBytes, _ := ioutil.ReadAll(stderr)
	if err != nil {
		return "", err
	}

	err = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("%s\n%s", err, string(stderrBytes))
	}

	scanner := bufio.NewScanner(bytes.NewReader(stdoutBytes))
	for scanner.Scan() {
		line := scanner.Text()
		keyVal := strings.Split(line, "=")
		if len(keyVal) != 2 {
			continue
		}

		key := strings.TrimSpace(keyVal[0])
		if key != "BINDIR" {
			continue
		}

		val := strings.TrimSpace(keyVal[1])
		return filepath.Join(val, "pg_ctl"), nil
	}

	return "", errors.New("could not find pg_ctl")
}
