//go:build linux && arm64

package sentryimport

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/checkpoint-restore/go-criu/v8/crit"
	core "github.com/checkpoint-restore/go-criu/v8/crit/images/criu-core"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/fdinfo"
	mmimage "github.com/checkpoint-restore/go-criu/v8/crit/images/mm"
	"github.com/checkpoint-restore/go-criu/v8/crit/images/pstree"
	"google.golang.org/protobuf/proto"
)

type checkpointImage struct {
	dir     string
	process *pstree.PstreeEntry
	threads []*core.CoreEntry
	memory  *mmimage.MmEntry
	fds     []*fdinfo.FdinfoEntry
	files   map[uint32]*fdinfo.FileEntry
}

func decodeImage[T proto.Message](dir, name string, entry T) ([]T, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := crit.New(f, nil, dir, false, false).Decode(entry)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	entries := make([]T, len(img.Entries))
	for i, item := range img.Entries {
		value, ok := item.Message.(T)
		if !ok {
			return nil, fmt.Errorf("%s entry %d: unexpected %T", name, i, item.Message)
		}
		entries[i] = value
	}
	return entries, nil
}

func readCheckpoint(dir string) (*checkpointImage, error) {
	processes, err := decodeImage(dir, "pstree.img", &pstree.PstreeEntry{})
	if err != nil {
		return nil, err
	}
	if len(processes) != 1 {
		return nil, fmt.Errorf("experiment requires one process, got %d", len(processes))
	}
	s := &checkpointImage{dir: dir, process: processes[0], files: make(map[uint32]*fdinfo.FileEntry)}
	for _, tid := range s.process.Threads {
		entries, err := decodeImage(dir, fmt.Sprintf("core-%d.img", tid), &core.CoreEntry{})
		if err != nil {
			return nil, err
		}
		if len(entries) != 1 || entries[0].GetMtype() != core.CoreEntry_AARCH64 {
			return nil, fmt.Errorf("TID %d: expected one ARM64 core", tid)
		}
		s.threads = append(s.threads, entries[0])
	}
	entries, err := decodeImage(dir, fmt.Sprintf("mm-%d.img", s.process.GetPid()), &mmimage.MmEntry{})
	if err != nil {
		return nil, err
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("expected one mm entry")
	}
	s.memory = entries[0]
	ids, err := decodeImage(dir, fmt.Sprintf("ids-%d.img", s.process.GetPid()), &core.TaskKobjIdsEntry{})
	if err != nil {
		return nil, err
	}
	if len(ids) != 1 {
		return nil, fmt.Errorf("expected one object-ID entry")
	}
	s.fds, err = decodeImage(dir, fmt.Sprintf("fdinfo-%d.img", ids[0].GetFilesId()), &fdinfo.FdinfoEntry{})
	if err != nil {
		return nil, err
	}
	files, err := decodeImage(dir, "files.img", &fdinfo.FileEntry{})
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		s.files[f.GetId()] = f
	}
	needed := map[uint32]bool{s.memory.GetExeFileId(): true}
	for _, f := range s.fds {
		needed[f.GetId()] = true
	}
	for _, v := range s.memory.Vmas {
		if v.GetShmid() != 0 {
			needed[uint32(v.GetShmid())] = true
		}
	}
	for id := range s.files {
		if !needed[id] {
			delete(s.files, id)
		}
	}
	return s, nil
}
