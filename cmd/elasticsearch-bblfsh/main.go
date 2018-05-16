package main

import (
	"fmt"
	"os"
  "path"
  "path/filepath"
	"reflect"
	"strings"
  "encoding/json"
  "io/ioutil"

	"gopkg.in/bblfsh/client-go.v2"
	"gopkg.in/bblfsh/client-go.v2/tools"
	"gopkg.in/bblfsh/sdk.v1/uast"
)

func getRawName(node *uast.Node) string {
	nameQuery := "//FieldDeclaration/VariableDeclarationFragment/SimpleName"
	nameNode, _ := tools.Filter(node, nameQuery)

	if len(nameNode) > 0 {
		return nameNode[0].Token
	} else {
		return ""
	}
}

func getType(node *uast.Node) string {
	typeQuery := "//FieldDeclaration/ParameterizedType/SimpleType[@internalRole='typeArguments']/SimpleName"
	nestedTypeQuery := "//FieldDeclaration/ParameterizedType/ParameterizedType[@internalRole='typeArguments']/*"

	typeNode, _ := tools.Filter(node, typeQuery)
	if len(typeNode) > 0 {
		return typeNode[0].Token
	} else {
    nestedTypeNodes, _ := tools.Filter(node, nestedTypeQuery)

    var nestedTypes []string
    for _, nestedNode := range nestedTypeNodes {
      nestedTypes = append(nestedTypes, nestedNode.Children[0].Token)
    }

		return strings.Join(nestedTypes, " of ")
	}
}

func getArguments(node *uast.Node) []*uast.Node {
	// Sometimes settings are created from a helper method, so they're considered a method
	// i.e. Setting.boolSetting("indices.query.query_string.allowLeadingWildcard", true, Property.NodeScope);
	// So the arguments are method arguments
	// But sometimes they are constructed new
	// i.e new Setting<>("index.translog.durability", Translog.Durability.REQUEST.name(),
	// So the arguments are part of the class construction

	methodArgumentsQuery := "//FieldDeclaration/VariableDeclarationFragment/MethodInvocation/*[@internalRole='arguments']"
	classArgumentsQuery := "//FieldDeclaration/VariableDeclarationFragment/ClassInstanceCreation/*[@internalRole='arguments']"

	methodArgumentNodes, _ := tools.Filter(node, methodArgumentsQuery)

	if len(methodArgumentNodes) > 0 {
		return methodArgumentNodes
	} else {
		classArguementNodes, _ := tools.Filter(node, classArgumentsQuery)
		return classArguementNodes
	}
}

func getSettingProperties(nodes []*uast.Node) []string {
	// Sometimes, settings are defined as "Setting.Property.Dynamic"
	// And sometimes as just "Property.Dynamic"
	// We're trying to pull out just the "Dynamic" part, so we we have two different queries
	// to try the fully qualified "long" way vs the shorter definition
	shortSettingPropertiesQuery := "//QualifiedName/SimpleName[@token='Property']/../SimpleName[@internalRole='name']"
	longSettingPropertiesQuery := "//QualifiedName/QualifiedName/SimpleName[@token='Property']/../../SimpleName[@internalRole='name']"

	var props []string

	for _, propNode := range nodes {
		longSettingPropertyNodes, _ := tools.Filter(propNode, longSettingPropertiesQuery)

		if len(longSettingPropertyNodes) > 0 {
			for _, prop := range longSettingPropertyNodes {
				props = append(props, prop.Token)
			}
		} else {
			shortSettingPropertyNodes, _ := tools.Filter(propNode, shortSettingPropertiesQuery)

			if len(shortSettingPropertyNodes) > 0 {
				for _, prop := range shortSettingPropertyNodes {
					props = append(props, prop.Token)
				}
			}
		}
	}

	return props
}

func getDefaultArg(node *uast.Node) string {
	var defaultArg string

	switch node.InternalType {
	case "NumberLiteral":
		defaultArg = fmt.Sprintf("%v", node.Properties["token"])
	case "BooleanLiteral":
		defaultArg = fmt.Sprintf("%v", node.Properties["booleanValue"])
	case "MethodInvocation":
		var arguments []string
		for _, child := range node.Children {
			switch child.InternalType {
			case "NumberLiteral":
				arguments = append(arguments, child.Properties["token"])
			default:
				arguments = append(arguments, child.Token)
			}
		}
		defaultArg = strings.Join(arguments, "->")
	case "ClassInstanceCreation":
		var arguments []string
		for _, child := range node.Children {
			switch child.InternalType {
			case "NumberLiteral":
				arguments = append(arguments, child.Properties["token"])
			case "QualifiedName":
				var subArgs []string
				for _, subChild := range child.Children {
					subArgs = append(subArgs, subChild.Token)
				}
				arguments = append(arguments, strings.Join(subArgs, "."))
			}
		}
		defaultArg = strings.Join(arguments, "->")
	default:
		defaultArg = node.Token
	}

	return defaultArg
}

type ElasticsearchSetting struct {
  Name string
  RawName string
  JavaType string
  Properties []string
  DefaultArg string

  CodeLine uint32
  CodeFile string
}

func getSettings(rootNode *uast.Node, fileName string) ([]ElasticsearchSetting) {
	query := "//FieldDeclaration/ParameterizedType/SimpleType/SimpleName[@token='Setting']/../../.."
	nodes, _ := tools.Filter(rootNode, query)

  var settings []ElasticsearchSetting

	for _, n := range nodes {
		rawSettingName := getRawName(n)
		settingType := getType(n)

		argumentNodes := getArguments(n)

    if len(argumentNodes) > 2 {

      settingName := argumentNodes[0].Token
      defaultArg := getDefaultArg(argumentNodes[1])
      settingProperties := getSettingProperties(argumentNodes)


      relativeFilePath := path.Join(strings.Split(fileName, "/")[len(strings.Split(rootDir, "/")):]...)

      setting := ElasticsearchSetting{
        Name: settingName,
        RawName: rawSettingName,
        JavaType: settingType,
        Properties: settingProperties,
        DefaultArg: defaultArg,
        CodeLine: n.StartPosition.Line,
        CodeFile: relativeFilePath}

      settings = append(settings, setting)
    } else {
      fmt.Errorf("Problem with %v", rawSettingName)
    }
	}

  return settings
}

var elasticsearchSettings []ElasticsearchSetting
var bblfshClient *bblfsh.Client
var rootDir string

func processFile(filePath string, info os.FileInfo, err error) error {
  if err != nil {
    return err
  }

  if !info.IsDir() && path.Ext(filePath) == ".java" {
    if err != nil {
      panic(err)
    }
    res, err := bblfshClient.NewParseRequest().ReadFile(filePath).Do()
    if err != nil {
      panic(err)
    }
    if reflect.TypeOf(res.UAST).Name() != "Node" {
      fmt.Errorf("Node must be the root of a UAST")
    }

    settings := getSettings(res.UAST, filePath)
    elasticsearchSettings = append(elasticsearchSettings, settings...)
  }

  return nil
}

func main() {
  client, _ := bblfsh.NewClient("localhost:9432")
  bblfshClient = client
  rootDir = "/home/nick/personal/elasticsearch"
  err := filepath.Walk(path.Join(rootDir, "server", "src", "main", "java", "org", "elasticsearch"), processFile)
  if err != nil {
    panic(err)
  }

  b, _ := json.Marshal(elasticsearchSettings)

  err = ioutil.WriteFile("elasticsearchSettings.json", b, 0644)
}
