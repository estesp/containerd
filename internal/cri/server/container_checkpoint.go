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
	"path/filepath"
	"strings"
	"time"

	crmetadata "github.com/checkpoint-restore/checkpointctl/lib"
	"github.com/containerd/containerd/v2/core/runtime/v2/runc/options"
	"github.com/containerd/containerd/v2/plugins"

	"github.com/containerd/containerd/v2/client"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (c *criService) CheckpointContainer(
	ctx context.Context,
	r *runtime.CheckpointContainerRequest,
) (*runtime.CheckpointContainerResponse, error) {
	start := time.Now()

	// Kubernetes has the possibility to request a file system local
	// checkpoint archive. If the given location starts with a '/' or
	// does not contain any slashes this assumes a local file.
	// Only slashes in the middle assumes a destination in the local image store.
	// Local file: archive.tar or /tmp/archive.tar
	// Image: localhost/checkpoint-image:tag
	exportToArchive := strings.HasPrefix(r.GetLocation(), "/") || !strings.Contains(r.GetLocation(), "/")

	container, err := c.containerStore.Get(r.GetContainerId())
	if err != nil {
		return nil, fmt.Errorf(
			"an error occurred when try to find container %q: %w",
			r.GetContainerId(),
			err,
		)
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
		return nil, fmt.Errorf("generating container config json failed: %w", err)
	}

	task, err := container.Container.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to get task for container %q: %w",
			r.GetContainerId(),
			err,
		)
	}
	_, err = task.Checkpoint(
		ctx,
		[]client.CheckpointTaskOpts{withCheckpointOpts(
			i.Runtime.Name,
			r.GetLocation(),
			c.getContainerRootDir(r.GetContainerId()),
			string(configJSON),
			exportToArchive,
		)}...,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"checkpointing container %q failed: %w",
			r.GetContainerId(),
			err,
		)
	}

	containerCheckpointTimer.WithValues(i.Runtime.Name).UpdateSince(start)

	return &runtime.CheckpointContainerResponse{}, nil
}

func withCheckpointOpts(
	rt, location, rootDir, configJSON string,
	exportToArchive bool,
) client.CheckpointTaskOpts {
	return func(r *client.CheckpointTaskInfo) error {
		// There is a check in the RPC interface to ensure 'location'
		// contains an image destination in the local image store.
		r.Name = location
		// Kubernetes currently supports checkpointing of container
		// as part of the Forensic Container Checkpointing KEP.
		// This implies that the container is never stopped
		leaveRunning := true

		r.ExportToArchive = exportToArchive
		r.ContainerConfig = configJSON

		switch rt {
		case plugins.RuntimeRuncV2:
			if r.Options == nil {
				r.Options = &options.CheckpointOptions{}
			}
			opts, _ := r.Options.(*options.CheckpointOptions)

			opts.Exit = !leaveRunning
			opts.WorkPath = rootDir
			if exportToArchive {
				opts.ImagePath = filepath.Join(
					rootDir,
					crmetadata.CheckpointDirectory,
				)
			}
		}
		return nil
	}
}
