/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	crmetadata "github.com/checkpoint-restore/checkpointctl/lib"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/runtime/v2/runc/options"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/containerd/containerd/v2/plugins"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/containerd/containerd/v2/client"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (c *criService) CheckpointContainer(ctx context.Context, r *runtime.CheckpointContainerRequest) (*runtime.CheckpointContainerResponse, error) {
	start := time.Now()

	container, err := c.containerStore.Get(r.GetContainerId())
	if err != nil {
		return nil, fmt.Errorf("an error occurred when try to find container %q: %w", r.GetContainerId(), err)
	}

	state := container.Status.Get().State()
	if state != runtime.ContainerState_CONTAINER_RUNNING {
		return nil, fmt.Errorf(
			"container %q is in %s state. only %s containers can be checkpointed",
			r.GetContainerId(),
			criContainerStateToString(state),
			criContainerStateToString(runtime.ContainerState_CONTAINER_RUNNING),
		)
	}

	imageRef := container.ImageRef
	image, err := c.GetImage(imageRef)
	if err != nil {
		return nil, fmt.Errorf("getting container image failed: %w", err)
	}

	i, err := container.Container.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("get container info: %w", err)
	}

	configJSON, err := json.Marshal(&crmetadata.ContainerConfig{
		ID:   container.ID,
		Name: container.Name,
		RootfsImageName: func() string {
			if len(image.References) > 0 {
				return image.References[0]
			}
			return ""
		}(),
		RootfsImageRef: imageRef,
		OCIRuntime:     i.Runtime.Name,
		RootfsImage:    container.Config.GetImage().UserSpecifiedImage,
		CheckpointedAt: time.Now(),
		CreatedTime:    i.CreatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("generating container config JSON failed: %w", err)
	}

	task, err := container.Container.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get task for container %q: %w", r.GetContainerId(), err)
	}
	img, err := task.Checkpoint(ctx, []client.CheckpointTaskOpts{withCheckpointOpts(i.Runtime.Name, c.getContainerRootDir(r.GetContainerId()))}...)
	if err != nil {
		return nil, fmt.Errorf("checkpointing container %q failed: %w", r.GetContainerId(), err)
	}

	// the checkpoint image has been provided as an index with manifests representing the tar of criu data, the rw layer, and the config

	var (
		index        v1.Index
		rawIndex     []byte
		targetDesc   = img.Target()
		contentStore = img.ContentStore()
	)

	rawIndex, err = content.ReadBlob(ctx, contentStore, targetDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve checkpoint index blob from content store: %w", err)
	}
	if err = json.Unmarshal(rawIndex, &index); err != nil {
		return nil, fmt.Errorf("failed to unmarshall blob into checkpoint data OCI index: %w", err)
	}

	cpPath := filepath.Join(c.getContainerRootDir(r.GetContainerId()), crmetadata.CheckpointDirectory)
	if err := os.MkdirAll(cpPath, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(cpPath)

	// walk the manifests and pull out the blobs that we need to save in the checkpoint tarball:
	// - the checkpoint criu data
	// - the rw diff tarball
	// - the spec blob
	for _, manifest := range index.Manifests {
		switch manifest.MediaType {
		case v1.MediaTypeImageLayerGzip:
			// the rw layer tarball
			if err := writeCheckpointFile(ctx, contentStore, manifest, filepath.Join(cpPath, crmetadata.RootFsDiffTar)); err != nil {
				return nil, fmt.Errorf("failed to copy rw filesystem layer blob to checkpoint dir: %w", err)
			}
		case images.MediaTypeContainerd1Checkpoint:
			// this is the criu data tarball
			if err := writeCheckpointFile(ctx, contentStore, manifest, filepath.Join(cpPath, "tmpcriu.tar")); err != nil {
				return nil, fmt.Errorf("failed to copy CRIU checkpoint blob to checkpoint dir: %w", err)
			}
		case images.MediaTypeContainerd1CheckpointConfig:
			// this is the container spec
			if err := writeCheckpointFile(ctx, contentStore, manifest, filepath.Join(cpPath, crmetadata.SpecDumpFile)); err != nil {
				return nil, fmt.Errorf("failed to copy container spec blob to checkpoint dir: %w", err)
			}
		default:
		}
	}

	if err := os.WriteFile(filepath.Join(cpPath, crmetadata.ConfigDumpFile), []byte(configJSON), 0o600); err != nil {
		return nil, err
	}

	// write final tarball of all content
	tar := archive.Diff(ctx, "", cpPath)

	outFile, err := os.OpenFile(r.Location, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	defer outFile.Close()
	_, err = io.Copy(outFile, tar)
	if err != nil {
		return nil, err
	}
	if err := tar.Close(); err != nil {
		return nil, err
	}

	containerCheckpointTimer.WithValues(i.Runtime.Name).UpdateSince(start)

	return &runtime.CheckpointContainerResponse{}, nil
}

func withCheckpointOpts(rt, rootDir string) client.CheckpointTaskOpts {
	return func(r *client.CheckpointTaskInfo) error {
		// Kubernetes currently supports checkpointing of container
		// as part of the Forensic Container Checkpointing KEP.
		// This implies that the container is never stopped
		leaveRunning := true

		switch rt {
		case plugins.RuntimeRuncV2:
			if r.Options == nil {
				r.Options = &options.CheckpointOptions{}
			}
			opts, _ := r.Options.(*options.CheckpointOptions)

			opts.Exit = !leaveRunning
			opts.WorkPath = rootDir
		}
		return nil
	}
}

func writeCheckpointFile(ctx context.Context, store content.Store, desc v1.Descriptor, fname string) error {
	ra, err := store.ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer ra.Close()
	f, err := os.Create(fname)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, content.NewReader(ra))
	if err != nil {
		return err
	}
	return nil
}
