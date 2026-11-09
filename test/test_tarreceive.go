package main

import (
	c "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
)

type TarreceiveSuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&TarreceiveSuite{})

// TestConvertWhitouts ensures that AUFS whiteouts are converted to OverlayFS
// whiteouts and have the same effect (i.e. hiding removed files)
func (s *TarreceiveSuite) TestConvertWhiteouts(t *c.C) {
	// build a Docker image with whiteouts
	repo := "tarreceive-test-whiteouts"
	s.buildDockerImage(t, repo,
		"RUN echo foo > /foo.txt",
		"RUN rm /foo.txt",
		"RUN mkdir /opaque && touch /opaque/file.txt",
		"RUN rm -rf /opaque && mkdir /opaque",
	)

	// create app
	client := s.controllerClient(t)
	app := &ct.App{Name: repo}
	t.Assert(client.CreateApp(app), c.IsNil)

	// flynn docker push image
	t.Assert(flynn(t, "/", "-a", app.Name, "docker", "push", repo), Succeeds)

	// check the whiteouts are effective
	t.Assert(flynn(t, "/", "-a", app.Name, "run", "sh", "-c", "[[ ! -f /foo.txt ]]"), Succeeds)
	t.Assert(flynn(t, "/", "-a", app.Name, "run", "sh", "-c", "[[ ! -f /opaque/file.txt ]]"), Succeeds)
}

// TestReleaseDeleteImageLayers ensures deleting one imported image does not
// remove another app's rootfs. Docker import squashes every image into its
// own layer, so the two artifacts do not share layer IDs.
func (s *TarreceiveSuite) TestReleaseDeleteImageLayers(t *c.C) {
	app1 := "tarreceive-test-delete-layers-1"
	s.buildDockerImage(t, app1,
		"RUN echo shared-layer > /shared.txt",
		"RUN echo app1-layer > /app1.txt",
	)
	t.Assert(flynn(t, "/", "create", "--remote", "", app1), Succeeds)
	t.Assert(flynn(t, "/", "-a", app1, "docker", "push", app1), Succeeds)

	app2 := "tarreceive-test-delete-layers-2"
	s.buildDockerImage(t, app2,
		"RUN echo shared-layer > /shared.txt",
		"RUN echo app2-layer > /app2.txt",
	)
	t.Assert(flynn(t, "/", "create", "--remote", "", app2), Succeeds)
	t.Assert(flynn(t, "/", "-a", app2, "docker", "push", app2), Succeeds)

	client := s.controllerClient(t)
	release1, err := client.GetAppRelease(app1)
	t.Assert(err, c.IsNil)
	image1, err := client.GetArtifact(release1.ArtifactIDs[0])
	t.Assert(err, c.IsNil)
	release2, err := client.GetAppRelease(app2)
	t.Assert(err, c.IsNil)
	image2, err := client.GetArtifact(release2.ArtifactIDs[0])
	t.Assert(err, c.IsNil)

	t.Assert(image1.Manifest().Rootfs[0].Layers, c.Not(c.HasLen), 0)
	t.Assert(image2.Manifest().Rootfs[0].Layers, c.Not(c.HasLen), 0)
	t.Assert(image1.ID, c.Not(c.Equals), image2.ID)
	t.Assert(image1.Manifest().Rootfs[0].Layers[0].ID, c.Not(c.Equals), image2.Manifest().Rootfs[0].Layers[0].ID)

	t.Assert(flynn(t, "/", "-a", app1, "delete", "--yes"), Succeeds)
	t.Assert(flynn(t, "/", "-a", app2, "run", "test", "-f", "/app2.txt"), Succeeds)

	t.Assert(flynn(t, "/", "-a", app2, "delete", "--yes"), Succeeds)
	app3 := "tarreceive-test-delete-layers-3"
	t.Assert(flynn(t, "/", "create", "--remote", "", app3), Succeeds)
	t.Assert(flynn(t, "/", "-a", app3, "docker", "push", app1), Succeeds)
	t.Assert(flynn(t, "/", "-a", app3, "run", "test", "-f", "/app1.txt"), Succeeds)
}

// TestTabsInEnv ensures that a docker container containing tabs
// in the environment variables can be imported.
func (s *TarreceiveSuite) TestTabsInEnv(t *c.C) {
	// build a Docker image with tabs in env
	repo := "tarreceive-test-tab-env"
	s.buildDockerImage(t, repo,
		"ENV TAB test\ttest",
	)

	// create app
	client := s.controllerClient(t)
	app := &ct.App{Name: repo}
	t.Assert(client.CreateApp(app), c.IsNil)

	// flynn docker push image
	t.Assert(flynn(t, "/", "-a", app.Name, "docker", "push", repo), Succeeds)

	// check the environment variable has the correct value
	t.Assert(flynn(t, "/", "-a", app.Name, "run", "sh", "-c", "case \"$TAB\" in 'test	test') exit 0;; *) exit 1;; esac"), Succeeds)
}
