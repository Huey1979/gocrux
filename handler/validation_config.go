package handler

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ============================================================
// YAML 校验配置加载
// ============================================================

// validationConfigFile YAML 文件格式。
type validationConfigFile struct {
	Validations map[string]*ValidateConfig `yaml:"validations"`
}

// LoadValidationConfig 从 YAML 文件加载校验规则配置。
//
// 返回 map[handlerName]*ValidateConfig，handlerName 对应注册表中的名称（如 "sys_site", "sys_role"）。
//
// YAML 格式：
//
//	validations:
//	  sys_site:
//	    create:
//	      site_code:
//	        required: true
//	    update:
//	      site_code:
//	        max_length: 64
//	    list:
//	      page_size:
//	        max: 200
//	      order_by:
//	        enum: ["site_code", "site_name", "created_at"]
//
// 使用方式：
//
//	vcMap, err := handler.LoadValidationConfig("configs/validations.yaml")
//	cfg := handler.HandlerConfig[entity.SysSite]{
//	    Validate: vcMap["sys_site"],
//	    // ...
//	}
func LoadValidationConfig(path string) (map[string]*ValidateConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取校验配置文件失败: %w", err)
	}

	var file validationConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析校验配置文件失败: %w", err)
	}

	return file.Validations, nil
}
