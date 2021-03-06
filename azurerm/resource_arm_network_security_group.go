package azurerm

import (
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

var networkSecurityGroupResourceName = "azurerm_network_security_group"

func resourceArmNetworkSecurityGroup() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmNetworkSecurityGroupCreate,
		Read:   resourceArmNetworkSecurityGroupRead,
		Update: resourceArmNetworkSecurityGroupCreate,
		Delete: resourceArmNetworkSecurityGroupDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"location": locationSchema(),

			"resource_group_name": resourceGroupNameSchema(),

			"security_rule": {
				Type:     schema.TypeList,
				Optional: true,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},

						"description": {
							Type:         schema.TypeString,
							Optional:     true,
							ValidateFunc: validateStringLength(140),
						},

						"protocol": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validateNetworkSecurityRuleProtocol,
							StateFunc:    ignoreCaseStateFunc,
						},

						"source_port_range": {
							Type:     schema.TypeString,
							Required: true,
						},

						"destination_port_range": {
							Type:     schema.TypeString,
							Required: true,
						},

						"source_address_prefix": {
							Type:     schema.TypeString,
							Required: true,
						},

						"destination_address_prefix": {
							Type:     schema.TypeString,
							Required: true,
						},

						"access": {
							Type:     schema.TypeString,
							Required: true,
							ValidateFunc: validation.StringInSlice([]string{
								string(network.SecurityRuleAccessAllow),
								string(network.SecurityRuleAccessDeny),
							}, true),
							DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
						},

						"priority": {
							Type:         schema.TypeInt,
							Required:     true,
							ValidateFunc: validation.IntBetween(100, 4096),
						},

						"direction": {
							Type:     schema.TypeString,
							Required: true,
							ValidateFunc: validation.StringInSlice([]string{
								string(network.SecurityRuleDirectionInbound),
								string(network.SecurityRuleDirectionOutbound),
							}, true),
							DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
						},
					},
				},
			},

			"tags": tagsSchema(),
		},
	}
}

func resourceArmNetworkSecurityGroupCreate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).secGroupClient

	name := d.Get("name").(string)
	location := d.Get("location").(string)
	resGroup := d.Get("resource_group_name").(string)
	tags := d.Get("tags").(map[string]interface{})

	sgRules, sgErr := expandAzureRmSecurityRules(d)
	if sgErr != nil {
		return fmt.Errorf("Error Building list of Network Security Group Rules: %+v", sgErr)
	}

	azureRMLockByName(name, networkSecurityGroupResourceName)
	defer azureRMUnlockByName(name, networkSecurityGroupResourceName)

	sg := network.SecurityGroup{
		Name:     &name,
		Location: &location,
		SecurityGroupPropertiesFormat: &network.SecurityGroupPropertiesFormat{
			SecurityRules: &sgRules,
		},
		Tags: expandTags(tags),
	}

	_, createErr := client.CreateOrUpdate(resGroup, name, sg, make(chan struct{}))
	err := <-createErr
	if err != nil {
		return err
	}

	read, err := client.Get(resGroup, name, "")
	if err != nil {
		return err
	}
	if read.ID == nil {
		return fmt.Errorf("Cannot read Virtual Network %q (resource group %q) ID", name, resGroup)
	}

	log.Printf("[DEBUG] Waiting for NSG (%q) to become available", name)
	stateConf := &resource.StateChangeConf{
		Pending:    []string{"Updating", "Creating"},
		Target:     []string{"Succeeded"},
		Refresh:    networkSecurityGroupStateRefreshFunc(client, resGroup, name),
		Timeout:    30 * time.Minute,
		MinTimeout: 15 * time.Second,
	}
	if _, err := stateConf.WaitForState(); err != nil {
		return fmt.Errorf("Error waiting for NSG (%q) to become available: %+v", name, err)
	}

	d.SetId(*read.ID)

	return resourceArmNetworkSecurityGroupRead(d, meta)
}

func resourceArmNetworkSecurityGroupRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).secGroupClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resGroup := id.ResourceGroup
	name := id.Path["networkSecurityGroups"]

	resp, err := client.Get(resGroup, name, "")
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error making Read request on Azure Network Security Group %q: %+v", name, err)
	}

	d.Set("name", resp.Name)
	d.Set("resource_group_name", resGroup)
	d.Set("location", azureRMNormalizeLocation(*resp.Location))

	if props := resp.SecurityGroupPropertiesFormat; props != nil {
		d.Set("security_rule", flattenNetworkSecurityRules(props.SecurityRules))
	}

	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmNetworkSecurityGroupDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).secGroupClient

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resGroup := id.ResourceGroup
	name := id.Path["networkSecurityGroups"]

	_, deleteErr := client.Delete(resGroup, name, make(chan struct{}))
	err = <-deleteErr

	return err
}

func flattenNetworkSecurityRules(rules *[]network.SecurityRule) []interface{} {
	result := make([]interface{}, 0)

	if rules != nil {
		for _, rule := range *rules {
			sgRule := make(map[string]interface{})
			sgRule["name"] = *rule.Name

			if props := rule.SecurityRulePropertiesFormat; props != nil {
				if props.DestinationAddressPrefix != nil {
					sgRule["destination_address_prefix"] = *props.DestinationAddressPrefix
				}
				if props.DestinationPortRange != nil {
					sgRule["destination_port_range"] = *props.DestinationPortRange
				}
				if props.SourceAddressPrefix != nil {
					sgRule["source_address_prefix"] = *props.SourceAddressPrefix
				}
				if props.SourcePortRange != nil {
					sgRule["source_port_range"] = *props.SourcePortRange
				}
				sgRule["priority"] = int(*props.Priority)
				sgRule["access"] = string(props.Access)
				sgRule["direction"] = string(props.Direction)
				sgRule["protocol"] = string(props.Protocol)

				if props.Description != nil {
					sgRule["description"] = *props.Description
				}
			}

			result = append(result, sgRule)
		}
	}

	return result
}

func expandAzureRmSecurityRules(d *schema.ResourceData) ([]network.SecurityRule, error) {
	sgRules := d.Get("security_rule").([]interface{})
	rules := make([]network.SecurityRule, 0)

	for _, sgRaw := range sgRules {
		data := sgRaw.(map[string]interface{})

		name := data["name"].(string)
		source_port_range := data["source_port_range"].(string)
		destination_port_range := data["destination_port_range"].(string)
		source_address_prefix := data["source_address_prefix"].(string)
		destination_address_prefix := data["destination_address_prefix"].(string)
		priority := int32(data["priority"].(int))
		access := data["access"].(string)
		direction := data["direction"].(string)
		protocol := data["protocol"].(string)

		properties := network.SecurityRulePropertiesFormat{
			SourcePortRange:          &source_port_range,
			DestinationPortRange:     &destination_port_range,
			SourceAddressPrefix:      &source_address_prefix,
			DestinationAddressPrefix: &destination_address_prefix,
			Priority:                 &priority,
			Access:                   network.SecurityRuleAccess(access),
			Direction:                network.SecurityRuleDirection(direction),
			Protocol:                 network.SecurityRuleProtocol(protocol),
		}

		if v := data["description"].(string); v != "" {
			properties.Description = &v
		}

		rule := network.SecurityRule{
			Name: &name,
			SecurityRulePropertiesFormat: &properties,
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

func networkSecurityGroupStateRefreshFunc(client network.SecurityGroupsClient, resourceGroupName string, sgName string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		res, err := client.Get(resourceGroupName, sgName, "")
		if err != nil {
			return nil, "", fmt.Errorf("Error issuing read request in networkSecurityGroupStateRefreshFunc for NSG '%s' (RG: '%s'): %+v", sgName, resourceGroupName, err)
		}

		return res, *res.SecurityGroupPropertiesFormat.ProvisioningState, nil
	}
}
