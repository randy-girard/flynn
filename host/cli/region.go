package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	Register("region", runRegion, `
usage: flynn-host region

Name this cluster and the clusters linked to it. A region is a separate
install, not another host on this Raft group.
`)
	Register("region:list", runRegionList, `
usage: flynn-host region:list

Print this cluster's region and linked clusters. This does not dial the peers.
`)
	Register("region:link", runRegionLink, `
usage: flynn-host region:link --region=<name> --domain=<domain> --tls-pin=<pin>

Record another cluster under <name>. The controller dials that cluster, checks
the TLS pin, and reads a response that is only the peer's region name. This
does not send CONTROLLER_KEY, does not store the peer's cluster key, and does
not copy users. Run it on each cluster that should list the other. At most
64 links. Cluster admin only (this host uses the cluster key).

Options:
	--region=<name>    peer region label
	--domain=<domain>  peer CLUSTER_DOMAIN
	--tls-pin=<pin>    sha256 pin of the peer controller certificate

Examples:

	$ flynn-host region:link --region EU --domain eu.example.com --tls-pin 'pin'
`)
	Register("region:unlink", runRegionUnlink, `
usage: flynn-host region:unlink <region>

Remove the link named <region>. This does not change the other cluster.
`)
}

func runRegion(_ *docopt.Args) error {
	fmt.Print(FormatHelp("region"))
	return nil
}

func regionLinkBody(regionName, domain, pin string) map[string]string {
	return map[string]string{
		"region":  regionName,
		"domain":  domain,
		"tls_pin": pin,
	}
}

func formatRegionList(list ct.ClusterRegionList) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REGION\tDOMAIN\tCONTROLLER\n")
	fmt.Fprintf(&b, "%s\t(this cluster)\t\n", list.Region)
	for _, link := range list.Links {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", link.Region, link.Domain, link.ControllerURL)
	}
	return b.String()
}

func putBootstrapRegion(name string) error {
	v, err := hostV1()
	if err != nil {
		return fmt.Errorf("set cluster region: %w", err)
	}
	// The response is {region, links}. A string map cannot decode links and
	// bootstrap would exit after the row was already stored.
	var list ct.ClusterRegionList
	if err := v.Put("/cluster/region", map[string]string{"region": name}, &list); err != nil {
		return fmt.Errorf("set cluster region: %w", err)
	}
	if list.Region != "" {
		name = list.Region
	}
	fmt.Printf("cluster region %s\n", name)
	return nil
}

func runRegionList(_ *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	var list ct.ClusterRegionList
	if err := v.Get("/cluster/regions", &list); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprint(w, formatRegionList(list))
	return w.Flush()
}

func runRegionLink(args *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	body := regionLinkBody(args.String["--region"], args.String["--domain"], args.String["--tls-pin"])
	var out ct.RegionLink
	if err := v.Post("/cluster/regions", body, &out); err != nil {
		return err
	}
	fmt.Printf("linked %s (%s)\n", out.Region, out.Domain)
	return nil
}

func runRegionUnlink(args *docopt.Args) error {
	name := strings.TrimSpace(args.String["<region>"])
	if name == "" {
		return fmt.Errorf("region is required")
	}
	v, err := hostV1()
	if err != nil {
		return err
	}
	if err := v.Delete("/cluster/regions/"+url.PathEscape(name), nil); err != nil {
		return err
	}
	fmt.Printf("unlinked %s\n", name)
	return nil
}
