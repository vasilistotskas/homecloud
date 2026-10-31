package api

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/internal/files"
)

// ListFileLocations implements AgentService.
func (s *Service) ListFileLocations(context.Context, *agentv3.ListFileLocationsRequest) (*agentv3.ListFileLocationsResponse, error) {
	locs := s.files.Locations()
	res := &agentv3.ListFileLocationsResponse{
		Locations:             make([]*agentv3.FileLocation, len(locs)),
		TrashRetentionSeconds: int64(s.files.TrashRetention().Seconds()),
	}
	for i, l := range locs {
		res.Locations[i] = &agentv3.FileLocation{
			Name: l.Name, Available: l.Available, FreeBytes: l.Free, UploadPath: files.UploadPath(l.Name),
		}
	}
	return res, nil
}

// ListFiles implements AgentService.
func (s *Service) ListFiles(_ context.Context, req *agentv3.ListFilesRequest) (*agentv3.ListFilesResponse, error) {
	entries, err := s.files.List(req.GetLocation(), req.GetPath())
	if err != nil {
		return nil, filesError(err)
	}
	res := &agentv3.ListFilesResponse{Entries: make([]*agentv3.FileEntry, len(entries))}
	for i, e := range entries {
		res.Entries[i] = &agentv3.FileEntry{
			Name: e.Name, Folder: e.Dir, SizeBytes: e.Size, ModifiedAt: timestamppb.New(e.Modified),
		}
	}
	return res, nil
}

// CreateFolder implements AgentService.
func (s *Service) CreateFolder(ctx context.Context, req *agentv3.CreateFolderRequest) (*agentv3.CreateFolderResponse, error) {
	if err := s.files.CreateFolder(req.GetLocation(), req.GetPath(), req.GetName()); err != nil {
		return nil, filesError(err)
	}
	s.log.InfoContext(ctx, "folder created", "location", req.GetLocation(), "principal", principal(ctx))
	return &agentv3.CreateFolderResponse{}, nil
}

// RenameFile implements AgentService.
func (s *Service) RenameFile(ctx context.Context, req *agentv3.RenameFileRequest) (*agentv3.RenameFileResponse, error) {
	if err := s.files.Rename(req.GetLocation(), req.GetPath(), req.GetNewName()); err != nil {
		return nil, filesError(err)
	}
	s.log.InfoContext(ctx, "file renamed", "location", req.GetLocation(), "principal", principal(ctx))
	return &agentv3.RenameFileResponse{}, nil
}

// MoveFiles implements AgentService.
func (s *Service) MoveFiles(ctx context.Context, req *agentv3.MoveFilesRequest) (*agentv3.MoveFilesResponse, error) {
	if len(req.GetPaths()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "nothing to move")
	}
	n, err := s.files.Move(req.GetLocation(), req.GetPaths(), req.GetDestination())
	if n > 0 {
		s.log.InfoContext(ctx, "files moved", "location", req.GetLocation(), "count", n, "principal", principal(ctx))
	}
	if err != nil {
		return nil, filesError(err)
	}
	return &agentv3.MoveFilesResponse{Moved: int32(n)}, nil //nolint:gosec // bounded by the request's 1 MiB
}

// TrashFiles implements AgentService.
func (s *Service) TrashFiles(ctx context.Context, req *agentv3.TrashFilesRequest) (*agentv3.TrashFilesResponse, error) {
	if len(req.GetPaths()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "nothing to delete")
	}
	n, err := s.files.Trash(req.GetLocation(), req.GetPaths())
	if n > 0 {
		s.log.InfoContext(ctx, "files moved to the trash", "location", req.GetLocation(), "count", n, "principal", principal(ctx))
	}
	if err != nil {
		return nil, filesError(err)
	}
	return &agentv3.TrashFilesResponse{Trashed: int32(n)}, nil //nolint:gosec // bounded by the request's 1 MiB
}

// ListTrash implements AgentService.
func (s *Service) ListTrash(_ context.Context, req *agentv3.ListTrashRequest) (*agentv3.ListTrashResponse, error) {
	items, err := s.files.ListTrash(req.GetLocation())
	if err != nil {
		return nil, filesError(err)
	}
	res := &agentv3.ListTrashResponse{Items: make([]*agentv3.TrashItem, len(items))}
	for i, it := range items {
		res.Items[i] = &agentv3.TrashItem{
			Id: it.ID, Path: it.Path, Folder: it.Dir, SizeBytes: it.Size, DeletedAt: timestamppb.New(it.Deleted),
		}
	}
	return res, nil
}

// RestoreFiles implements AgentService.
func (s *Service) RestoreFiles(ctx context.Context, req *agentv3.RestoreFilesRequest) (*agentv3.RestoreFilesResponse, error) {
	if len(req.GetIds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, "nothing to restore")
	}
	paths, err := s.files.Restore(req.GetLocation(), req.GetIds())
	if len(paths) > 0 {
		s.log.InfoContext(ctx, "files restored from the trash", "location", req.GetLocation(), "count", len(paths), "principal", principal(ctx))
	}
	if err != nil {
		return nil, filesError(err)
	}
	return &agentv3.RestoreFilesResponse{Paths: paths}, nil
}

// GetFileLink implements AgentService.
func (s *Service) GetFileLink(ctx context.Context, req *agentv3.GetFileLinkRequest) (*agentv3.GetFileLinkResponse, error) {
	f, _, err := s.files.Open(req.GetLocation(), req.GetPath())
	if err != nil {
		return nil, filesError(err)
	}
	if err := f.Close(); err != nil {
		return nil, internal(err)
	}
	link := s.media.Sign(principal(ctx), files.DownloadPath(req.GetLocation(), req.GetPath()))
	if req.GetInline() {
		link += "&inline=1"
	}
	return &agentv3.GetFileLinkResponse{Url: link}, nil
}

func filesError(err error) error {
	switch {
	case errors.Is(err, files.ErrNoLocation), errors.Is(err, files.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	case errors.Is(err, files.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err.Error()).WithCause(err)
	case errors.Is(err, files.ErrExists):
		return connect.NewError(connect.CodeAlreadyExists, err.Error()).WithCause(err)
	case errors.Is(err, files.ErrUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error()).WithCause(err)
	default:
		return internal(err)
	}
}
