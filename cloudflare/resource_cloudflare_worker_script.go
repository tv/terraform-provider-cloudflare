package cloudflare

import (
	"fmt"
	"log"
	"strings"

	cloudflare "github.com/cloudflare/cloudflare-go"
	"github.com/hashicorp/terraform-plugin-sdk/helper/hashcode"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/pkg/errors"
)

func resourceCloudflareWorkerScript() *schema.Resource {
	return &schema.Resource{
		Create: resourceCloudflareWorkerScriptCreate,
		Read:   resourceCloudflareWorkerScriptRead,
		Update: resourceCloudflareWorkerScriptUpdate,
		Delete: resourceCloudflareWorkerScriptDelete,
		Importer: &schema.ResourceImporter{
			State: resourceCloudflareWorkerScriptImport,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"content": {
				Type:     schema.TypeString,
				Required: true,
			},
			"binding": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},
						"kv_namespace_id": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"plain_text": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"secret_text": {
							Type:      schema.TypeString,
							Sensitive: true,
							Optional:  true,
						},
					},
				},
				Set: resourceCloudflareWorkerScriptBindingHash,
			},
			"kv_namespace_binding": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},
						"namespace_id": {
							Type:     schema.TypeString,
							Required: true,
						},
					},
				},
				Set: resourceCloudflareWorkerScriptKvNamespaceBindingHash,
			},
		},
	}
}

func resourceCloudflareWorkerScriptBindingHash(v interface{}) int {
	m := v.(map[string]interface{})
	name := m["name"].(string)
	if v := m["kv_namespace_id"].(string); v != "" {
		return hashcode.String(fmt.Sprintf("%s-%s", name, v))
	}
	if v := m["plain_text"].(string); v != "" {
		return hashcode.String(fmt.Sprintf("%s-%s", name, v))
	}
	if v := m["secret_text"].(string); v != "" {
		return hashcode.String(fmt.Sprintf("%s-%s", name, v))
	}
	return 0
}

func resourceCloudflareWorkerScriptKvNamespaceBindingHash(v interface{}) int {
	m := v.(map[string]interface{})

	return hashcode.String(fmt.Sprintf("%s-%s", m["name"].(string), m["namespace_id"].(string)))
}

type ScriptData struct {
	// The script id will be the `name` for named script
	// or the `zone_name` for zone-scoped scripts
	ID     string
	Params cloudflare.WorkerRequestParams
}

func getScriptData(d *schema.ResourceData, client *cloudflare.API) (ScriptData, error) {
	scriptName := d.Get("name").(string)

	params := cloudflare.WorkerRequestParams{
		ScriptName: scriptName,
	}

	return ScriptData{
		scriptName,
		params,
	}, nil
}

type ScriptBindings map[string]cloudflare.WorkerBinding

func getWorkerScriptBindings(scriptName string, client *cloudflare.API) (ScriptBindings, error) {
	resp, err := client.ListWorkerBindings(&cloudflare.WorkerRequestParams{ScriptName: scriptName})
	if err != nil {
		return nil, fmt.Errorf("cannot list script bindings: %v", err)
	}

	bindings := make(ScriptBindings, len(resp.BindingList))

	for _, b := range resp.BindingList {
		bindings[b.Name] = b.Binding
	}

	return bindings, nil
}

func parseWorkerBindings(d *schema.ResourceData, bindings ScriptBindings) {
	for _, rawData := range d.Get("binding").(*schema.Set).List() {
		data := rawData.(map[string]interface{})
		bindings[data["name"].(string)] = parseWorkerBinding(data)
	}

	for _, rawData := range d.Get("kv_namespace_binding").(*schema.Set).List() {
		data := rawData.(map[string]interface{})
		bindings[data["name"].(string)] = cloudflare.WorkerKvNamespaceBinding{
			NamespaceID: data["namespace_id"].(string),
		}
	}
}

func parseWorkerBinding(data map[string]interface{}) cloudflare.WorkerBinding {
	if v := data["kv_namespace_id"].(string); v != "" {
		return cloudflare.WorkerKvNamespaceBinding{
			NamespaceID: v,
		}
	}
	if v := data["plain_text"].(string); v != "" {
		return cloudflare.WorkerPlainTextBinding{
			Text: v,
		}
	}
	if v := data["secret_text"].(string); v != "" {
		return cloudflare.WorkerSecretTextBinding{
			Text: v,
		}
	}
	return nil
}

func resourceCloudflareWorkerScriptCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*cloudflare.API)

	scriptData, err := getScriptData(d, client)
	if err != nil {
		return err
	}

	// make sure that the worker does not already exist
	r, _ := client.DownloadWorker(&scriptData.Params)
	if r.WorkerScript.Script != "" {
		return fmt.Errorf("script already exists")
	}

	scriptBody := d.Get("content").(string)
	if scriptBody == "" {
		return fmt.Errorf("script content cannot be empty")
	}

	log.Printf("[INFO] Creating Cloudflare Worker Script from struct: %+v", &scriptData.Params)

	bindings := make(ScriptBindings)

	parseWorkerBindings(d, bindings)

	scriptParams := cloudflare.WorkerScriptParams{
		Script:   scriptBody,
		Bindings: bindings,
	}

	_, err = client.UploadWorkerWithBindings(&scriptData.Params, &scriptParams)
	if err != nil {
		return errors.Wrap(err, "error creating worker script")
	}

	d.SetId(scriptData.ID)

	return nil
}

func resourceCloudflareWorkerScriptRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*cloudflare.API)

	scriptData, err := getScriptData(d, client)
	if err != nil {
		return err
	}

	r, err := client.DownloadWorker(&scriptData.Params)
	if err != nil {
		// If the resource is deleted, we should set the ID to "" and not
		// return an error according to the terraform spec
		if strings.Contains(err.Error(), "HTTP status 404") {
			d.SetId("")
			return nil
		}

		return errors.Wrap(err,
			fmt.Sprintf("Error reading worker script from API for resource %+v", &scriptData.Params))
	}

	bindings, err := getWorkerScriptBindings(d.Get("name").(string), client)
	if err != nil {
		return err
	}

	workerBindings := &schema.Set{
		F: resourceCloudflareWorkerScriptBindingHash,
	}

	for name, binding := range bindings {
		switch v := binding.(type) {
		case cloudflare.WorkerKvNamespaceBinding:
			workerBindings.Add(map[string]interface{}{
				"name":            name,
				"kv_namespace_id": v.NamespaceID,
			})
		case cloudflare.WorkerPlainTextBinding:
			workerBindings.Add(map[string]interface{}{
				"name":       name,
				"plain_text": v.Text,
			})
		case cloudflare.WorkerSecretTextBinding:
			workerBindings.Add(map[string]interface{}{
				"name":        name,
				"secret_text": v.Text,
			})
		}
	}

	_ = d.Set("content", r.Script)

	if err := d.Set("binding", workerBindings); err != nil {
		return fmt.Errorf("cannot set bindings (%s): %v", d.Id(), err)
	}

	return nil
}

func resourceCloudflareWorkerScriptUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*cloudflare.API)

	scriptData, err := getScriptData(d, client)
	if err != nil {
		return err
	}

	scriptBody := d.Get("content").(string)
	if scriptBody == "" {
		return fmt.Errorf("script content cannot be empty")
	}

	log.Printf("[INFO] Updating Cloudflare Worker Script from struct: %+v", &scriptData.Params)

	bindings := make(ScriptBindings)

	parseWorkerBindings(d, bindings)

	scriptParams := cloudflare.WorkerScriptParams{
		Script:   scriptBody,
		Bindings: bindings,
	}

	_, err = client.UploadWorkerWithBindings(&scriptData.Params, &scriptParams)
	if err != nil {
		return errors.Wrap(err, "error updating worker script")
	}

	return nil
}

func resourceCloudflareWorkerScriptDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*cloudflare.API)

	scriptData, err := getScriptData(d, client)
	if err != nil {
		return err
	}

	log.Printf("[INFO] Deleting Cloudflare Worker Script from struct: %+v", &scriptData.Params)

	_, err = client.DeleteWorker(&scriptData.Params)
	if err != nil {
		// If the resource is already deleted, we should return without an error
		// according to the terraform spec
		if strings.Contains(err.Error(), "HTTP status 404") {
			return nil
		}

		return errors.Wrap(err, "error deleting worker script")
	}

	return nil
}

func resourceCloudflareWorkerScriptImport(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	scriptID := d.Id()
	_ = d.Set("name", scriptID)

	_ = resourceCloudflareWorkerScriptRead(d, meta)

	return []*schema.ResourceData{d}, nil
}
