/*
Copyright (c) 2021 PaddlePaddle Authors. All Rights Reserve.

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

package pipeline

import (
	"encoding/base64"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/PaddlePaddle/PaddleFlow/pkg/apiserver/common"
	"github.com/PaddlePaddle/PaddleFlow/pkg/apiserver/handler"
	"github.com/PaddlePaddle/PaddleFlow/pkg/apiserver/models"
	"github.com/PaddlePaddle/PaddleFlow/pkg/apiserver/router/util"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/logger"
	"github.com/PaddlePaddle/PaddleFlow/pkg/common/schema"
	"github.com/PaddlePaddle/PaddleFlow/pkg/model"
	"github.com/PaddlePaddle/PaddleFlow/pkg/pipeline"
	pplcommon "github.com/PaddlePaddle/PaddleFlow/pkg/pipeline/common"
	"github.com/PaddlePaddle/PaddleFlow/pkg/storage"
)

type CreatePipelineRequest struct {
	FsName   string `json:"fsName"`
	YamlPath string `json:"yamlPath"` // optional,  use "./run.yaml" if not specified, one of 2 sources of run
	YamlRaw  string `json:"yamlRaw"`  // optional, one of 2 sources of run
	UserName string `json:"username"` // optional, only for root user
	Desc     string `json:"desc"`     // optional
}

type CreatePipelineResponse struct {
	PipelineID        string `json:"pipelineID"`
	PipelineVersionID string `json:"pipelineVersionID"`
	Name              string `json:"name"`
}

type UpdatePipelineRequest = CreatePipelineRequest

type UpdatePipelineResponse struct {
	PipelineID        string `json:"pipelineID"`
	PipelineVersionID string `json:"pipelineVersionID"`
}

type ListPipelineResponse struct {
	common.MarkerInfo
	PipelineList []PipelineBrief `json:"pipelineList"`
}

type GetPipelineResponse struct {
	Pipeline         PipelineBrief    `json:"pipeline"`
	PipelineVersions PipelineVersions `json:"pplVersions"`
}

type PipelineVersions struct {
	common.MarkerInfo
	PipelineVersionList []PipelineVersionBrief `json:"pplVersionList"`
}

type GetPipelineVersionResponse struct {
	Pipeline        PipelineBrief        `json:"pipeline"`
	PipelineVersion PipelineVersionBrief `json:"pipelineVersion"`
}

type PipelineBrief struct {
	ID         string `json:"pipelineID"`
	Name       string `json:"name"`
	Desc       string `json:"desc"`
	UserName   string `json:"username"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

func (pb *PipelineBrief) updateFromPipelineModel(pipeline model.Pipeline) {
	pb.ID = pipeline.ID
	pb.Name = pipeline.Name
	pb.Desc = pipeline.Desc
	pb.UserName = pipeline.UserName
	pb.CreateTime = pipeline.CreatedAt.Format("2006-01-02 15:04:05")
	pb.UpdateTime = pipeline.UpdatedAt.Format("2006-01-02 15:04:05")
}

type PipelineVersionBrief struct {
	ID           string `json:"pipelineVersionID"`
	PipelineID   string `json:"pipelineID"`
	FsName       string `json:"fsName"`
	YamlPath     string `json:"yamlPath"`
	PipelineYaml string `json:"pipelineYaml"`
	UserName     string `json:"username"`
	CreateTime   string `json:"createTime"`
	UpdateTime   string `json:"updateTime"`
}

func (pdb *PipelineVersionBrief) updateFromPipelineVersionModel(pipelineVersion model.PipelineVersion) {
	pdb.ID = pipelineVersion.ID
	pdb.PipelineID = pipelineVersion.PipelineID
	pdb.FsName = pipelineVersion.FsName
	pdb.YamlPath = pipelineVersion.YamlPath
	pdb.PipelineYaml = pipelineVersion.PipelineYaml
	pdb.UserName = pipelineVersion.UserName
	pdb.CreateTime = pipelineVersion.CreatedAt.Format("2006-01-02 15:04:05")
	pdb.UpdateTime = pipelineVersion.UpdatedAt.Format("2006-01-02 15:04:05")
}

func getPipelineYamlFromYamlRaw(ctx *logger.RequestContext, request *CreatePipelineRequest) ([]byte, error) {
	pipelineYaml, err := base64.StdEncoding.DecodeString(request.YamlRaw)
	if err != nil {
		err = fmt.Errorf("Decode raw yaml[%s] failed. err:%v", request.YamlRaw, err)
		return nil, err
	}

	return pipelineYaml, nil
}

func getPipelineYamlFromYamlPath(ctx *logger.RequestContext, request *CreatePipelineRequest) ([]byte, error) {
	if request.YamlPath == "" {
		request.YamlPath = "./run.yaml"
	}

	if request.FsName == "" {
		err := fmt.Errorf("cannot get pipeline: fsname shall not be empty while you specified YamlPath[%s]",
			request.YamlPath)
		return nil, err
	}

	fsID, err := CheckFsAndGetID(ctx.UserName, request.UserName, request.FsName)
	if err != nil {
		return nil, err
	}

	// read run.yaml
	pipelineYaml, err := handler.ReadFileFromFs(fsID, request.YamlPath, ctx.Logging())
	if err != nil {
		err := fmt.Errorf("readFileFromFs[%s] from fs[%s] failed. err:%v", request.YamlPath, fsID, err)
		return nil, err
	}

	return pipelineYaml, nil
}

func getPipelineYaml(ctx *logger.RequestContext, request *CreatePipelineRequest) ([]byte, error) {
	if request.YamlRaw != "" {
		if request.YamlPath != "" {
			err := fmt.Errorf("you can only specify one of YamlPath and YamlRaw")
			return nil, err
		}

		if request.FsName != "" {
			err := fmt.Errorf("you cannot specify FsName while you specified YamlRaw")
			return nil, err
		}
		return getPipelineYamlFromYamlRaw(ctx, request)
	}

	return getPipelineYamlFromYamlPath(ctx, request)
}

func CreatePipeline(ctx *logger.RequestContext, request CreatePipelineRequest) (CreatePipelineResponse, error) {
	// 校验desc长度
	if len(request.Desc) > util.MaxDescLength {
		ctx.ErrorCode = common.InvalidArguments
		errMsg := fmt.Sprintf("desc too long, should be less than %d", util.MaxDescLength)
		ctx.Logging().Errorf(errMsg)
		return CreatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	pipelineYaml, err := getPipelineYaml(ctx, &request)
	if err != nil {
		err = fmt.Errorf("create pipeline failed. err:%v", err)
		ctx.ErrorCode = common.InvalidArguments
		ctx.Logging().Error(err.Error())
		return CreatePipelineResponse{}, err
	}

	// validate pipeline and get name of pipeline
	// 此处同样会校验pipeline name格式（正则表达式为：`^[A-Za-z_][A-Za-z0-9_]{1,49}$`）
	pplName, err := validateWorkflowForPipeline(string(pipelineYaml), ctx.UserName, request.UserName)
	if err != nil {
		ctx.ErrorCode = common.MalformedYaml
		errMsg := fmt.Sprintf("validateWorkflowForPipeline failed. err:%v", err)
		ctx.Logging().Errorf(errMsg)
		return CreatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	// 校验pipeline是否存在，一个用户不能创建同名pipeline
	_, err = storage.Pipeline.GetPipeline(pplName, ctx.UserName)
	if err == nil {
		ctx.ErrorCode = common.DuplicatedName
		errMsg := fmt.Sprintf("CreatePipeline failed: user[%s] already has pipeline[%s], cannot create again, use update instead!", ctx.UserName, pplName)
		ctx.Logging().Errorf(errMsg)
		return CreatePipelineResponse{}, fmt.Errorf(errMsg)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("CreatePipeline failed: %s", err)
		ctx.Logging().Errorf(errMsg)
		return CreatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	// create Pipeline in db
	ppl := model.Pipeline{
		ID:       "", // to be back-filled according to db pk
		Name:     pplName,
		Desc:     request.Desc,
		UserName: ctx.UserName,
	}

	yamlMd5 := common.GetMD5Hash(pipelineYaml)

	// 这里主要是为了获取fsID，写入数据库中
	var fsID string
	if request.FsName != "" {
		fsID, err = CheckFsAndGetID(ctx.UserName, request.UserName, request.FsName)
		if err != nil {
			ctx.ErrorCode = common.InvalidArguments
			errMsg := fmt.Sprintf("Create Pipeline failed: %s", err)
			ctx.Logging().Errorf(errMsg)
		}
	}

	pplVersion := model.PipelineVersion{
		FsID:         fsID,
		FsName:       request.FsName,
		YamlPath:     request.YamlPath,
		PipelineYaml: string(pipelineYaml),
		PipelineMd5:  yamlMd5,
		UserName:     ctx.UserName,
	}

	pplID, pplVersionID, err := storage.Pipeline.CreatePipeline(ctx.Logging(), &ppl, &pplVersion)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("create pipeline failed inserting db. error:%s", err.Error())
		ctx.Logging().Errorf(errMsg)
		return CreatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	ctx.Logging().Debugf("create pipeline[%s] successful", pplID)
	response := CreatePipelineResponse{
		PipelineID:        pplID,
		PipelineVersionID: pplVersionID,
		Name:              pplName,
	}
	return response, nil
}

func UpdatePipeline(ctx *logger.RequestContext, request UpdatePipelineRequest, pipelineID string) (UpdatePipelineResponse, error) {
	// 校验desc长度
	if len(request.Desc) > util.MaxDescLength {
		ctx.ErrorCode = common.InvalidArguments
		errMsg := fmt.Sprintf("desc too long, should be less than %d", util.MaxDescLength)
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	pipelineYaml, err := getPipelineYaml(ctx, &request)
	if err != nil {
		ctx.ErrorCode = common.InvalidArguments
		err = fmt.Errorf("update pipeline failed. err:%v", err)
		ctx.Logging().Error(err.Error())
		return UpdatePipelineResponse{}, err
	}

	// validate pipeline and get name of pipeline
	pplName, err := validateWorkflowForPipeline(string(pipelineYaml), ctx.UserName, request.UserName)
	if err != nil {
		ctx.ErrorCode = common.MalformedYaml
		errMsg := fmt.Sprintf("validateWorkflowForPipeline failed. err:%v", err)
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	hasAuth, ppl, err := CheckPipelinePermission(ctx.UserName, pipelineID)
	if err != nil {
		ctx.ErrorCode = common.InvalidArguments
		errMsg := fmt.Sprintf("update pipeline[%s] failed. err:%v", pipelineID, err)
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	} else if !hasAuth {
		ctx.ErrorCode = common.AccessDenied
		errMsg := fmt.Sprintf("update pipeline[%s] failed. Access denied for user[%s]", pipelineID, ctx.UserName)
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	// 校验待更新的pipeline name，和数据库中pipeline name一致
	if ppl.Name != pplName {
		ctx.ErrorCode = common.InvalidArguments
		errMsg := fmt.Sprintf("update pipeline failed, pplname[%s] in yaml not the same as [%s] of pipeline[%s]", pplName, ppl.Name, pipelineID)
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	ppl.Desc = request.Desc
	yamlMd5 := common.GetMD5Hash(pipelineYaml)

	// 这里主要是为了获取fsID，写入数据库中
	var fsID string
	if request.FsName != "" {
		fsID, err = CheckFsAndGetID(ctx.UserName, request.UserName, request.FsName)
		if err != nil {
			ctx.ErrorCode = common.InternalError
			errMsg := fmt.Sprintf("CreatePipeline failed: %s", err)
			ctx.Logging().Errorf(errMsg)
		}
	}

	pplVersion := model.PipelineVersion{
		PipelineID:   pipelineID,
		FsID:         fsID,
		FsName:       request.FsName,
		YamlPath:     request.YamlPath,
		PipelineYaml: string(pipelineYaml),
		PipelineMd5:  yamlMd5,
		UserName:     ctx.UserName,
	}

	pplID, pplVersionID, err := storage.Pipeline.UpdatePipeline(ctx.Logging(), &ppl, &pplVersion)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("update pipeline failed inserting db. error:%s", err.Error())
		ctx.Logging().Errorf(errMsg)
		return UpdatePipelineResponse{}, fmt.Errorf(errMsg)
	}

	ctx.Logging().Debugf("update pipeline[%s] successful, pplVersionID[%s]", pplID, pplVersionID)
	response := UpdatePipelineResponse{
		PipelineID:        pplID,
		PipelineVersionID: pplVersionID,
	}
	return response, nil
}

// todo: 为了校验pipeline，需要准备的内容太多，需要简化校验逻辑
func validateWorkflowForPipeline(pipelineYaml string, ctxUsername string, reqUsername string) (name string, err error) {
	// parse yaml -> WorkflowSource
	wfs, err := schema.GetWorkflowSource([]byte(pipelineYaml))
	if err != nil {
		logger.Logger().Errorf("get WorkflowSource by yaml failed. yaml: %s \n, err:%v", pipelineYaml, err)
		return "", err
	}

	// fill extra info
	param := map[string]interface{}{}
	extra := map[string]string{
		pplcommon.WfExtraInfoKeyFSUserName: "",
	}

	if wfs.FsOptions.MainFS.Name != "" {
		extra[pplcommon.WfExtraInfoKeyFsName] = wfs.FsOptions.MainFS.Name

		fsID, err := CheckFsAndGetID(ctxUsername, reqUsername, wfs.FsOptions.MainFS.Name)
		if err != nil {
			logger.Logger().Errorf("check main fs in pipeline failed, err:%v", err)
			return "", err
		}

		extra[pplcommon.WfExtraInfoKeyFsID] = fsID
	}

	// validate
	wfCbs := pipeline.WorkflowCallbacks{
		UpdateRuntimeCb: func(string, interface{}) (int64, bool) { return 0, true },
		LogCacheCb:      LogCacheFunc,
		ListCacheCb:     ListCacheFunc,
	}

	// todo：这里为了校验，还要传特殊的run name（validatePipeline），可以想办法简化校验逻辑
	wfPtr, err := pipeline.NewWorkflow(wfs, "validatePipeline", param, extra, wfCbs)
	if err != nil {
		logger.Logger().Errorf("NewWorkflow for pipeline[%s] failed. err:%v", wfs.Name, err)
		return "", err
	}
	if wfPtr == nil {
		err := fmt.Errorf("NewWorkflow ptr for pipeline[%s] is nil", wfs.Name)
		logger.Logger().Errorln(err.Error())
		return "", err
	}
	return wfs.Name, nil
}

func ListPipeline(ctx *logger.RequestContext, marker string, maxKeys int, userFilter, nameFilter []string) (ListPipelineResponse, error) {
	ctx.Logging().Debugf("begin list pipeline.")

	var pk int64
	var err error
	if marker != "" {
		pk, err = common.DecryptPk(marker)
		if err != nil {
			ctx.ErrorCode = common.InvalidMarker
			errMsg := fmt.Sprintf("DecryptPk marker[%s] failed. err:[%s]", marker, err.Error())
			ctx.Logging().Errorf(errMsg)
			return ListPipelineResponse{}, fmt.Errorf(errMsg)
		}
	}

	// 只有root用户才能设置userFilter，否则只能查询当前普通用户创建的pipeline列表
	if !common.IsRootUser(ctx.UserName) {
		if len(userFilter) != 0 {
			ctx.ErrorCode = common.InvalidArguments
			errMsg := fmt.Sprint("only root user can set userFilter!")
			ctx.Logging().Errorf(errMsg)
			return ListPipelineResponse{}, fmt.Errorf(errMsg)
		} else {
			userFilter = []string{ctx.UserName}
		}
	}

	pipelineList, err := storage.Pipeline.ListPipeline(pk, maxKeys, userFilter, nameFilter)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		ctx.Logging().Errorf("ListPipeline[%d-%s-%s] failed. err: %v", maxKeys, userFilter, nameFilter, err)
		return ListPipelineResponse{}, err
	}

	listPipelineResponse := ListPipelineResponse{
		PipelineList: []PipelineBrief{},
	}

	// get next marker
	listPipelineResponse.IsTruncated = false
	if len(pipelineList) > 0 {
		ppl := pipelineList[len(pipelineList)-1]
		isLastPk, err := storage.Pipeline.IsLastPipelinePk(ctx.Logging(), ppl.Pk, userFilter, nameFilter)
		if err != nil {
			ctx.ErrorCode = common.InternalError
			errMsg := fmt.Sprintf("get last pipeline Pk failed. err:[%s]", err.Error())
			ctx.Logging().Errorf(errMsg)
			return ListPipelineResponse{}, fmt.Errorf(errMsg)
		}

		if !isLastPk {
			nextMarker, err := common.EncryptPk(ppl.Pk)
			if err != nil {
				ctx.ErrorCode = common.InternalError
				errMsg := fmt.Sprintf("EncryptPk error. pk:[%d] error:[%s]", ppl.Pk, err.Error())
				ctx.Logging().Errorf(errMsg)
				return ListPipelineResponse{}, fmt.Errorf(errMsg)
			}
			listPipelineResponse.NextMarker = nextMarker
			listPipelineResponse.IsTruncated = true
		}
	}

	listPipelineResponse.MaxKeys = maxKeys
	for _, ppl := range pipelineList {
		pplBrief := PipelineBrief{}
		pplBrief.updateFromPipelineModel(ppl)
		listPipelineResponse.PipelineList = append(listPipelineResponse.PipelineList, pplBrief)
	}
	return listPipelineResponse, nil
}

func GetPipeline(ctx *logger.RequestContext, pipelineID, marker string, maxKeys int, fsFilter []string) (GetPipelineResponse, error) {
	ctx.Logging().Debugf("begin get pipeline.")
	getPipelineResponse := GetPipelineResponse{}

	// query pipeline
	ppl, err := storage.Pipeline.GetPipelineByID(pipelineID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ctx.ErrorCode = common.InvalidArguments
		} else {
			ctx.ErrorCode = common.InternalError
		}
		errMsg := fmt.Sprintf("get pipeline[%s] failed, err: %v", pipelineID, err)
		ctx.Logging().Errorf(errMsg)
		return GetPipelineResponse{}, fmt.Errorf(errMsg)
	}

	if !common.IsRootUser(ctx.UserName) && ctx.UserName != ppl.UserName {
		ctx.ErrorCode = common.AccessDenied
		err := common.NoAccessError(ctx.UserName, common.ResourceTypePipeline, pipelineID)
		ctx.Logging().Errorln(err.Error())
		return GetPipelineResponse{}, err
	}
	getPipelineResponse.Pipeline.updateFromPipelineModel(ppl)

	// query pipeline version
	var pk int64
	if marker != "" {
		pk, err = common.DecryptPk(marker)
		if err != nil {
			ctx.ErrorCode = common.InvalidMarker
			errMsg := fmt.Sprintf("DecryptPk marker[%s] failed. err:[%s]", marker, err.Error())
			ctx.Logging().Errorf(errMsg)
			return GetPipelineResponse{}, fmt.Errorf(errMsg)
		}
	}

	pipelineVersionList, err := storage.Pipeline.ListPipelineVersion(pipelineID, pk, maxKeys, fsFilter)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		ctx.Logging().Errorf("get Pipeline version[%s-%d-%d-%s]. err: %v", pipelineID, pk, maxKeys, fsFilter, err)
		return GetPipelineResponse{}, err
	}

	// get next marker
	pipelineVersions := PipelineVersions{}
	pipelineVersions.IsTruncated = false
	if len(pipelineVersionList) > 0 {
		pplVersion := pipelineVersionList[len(pipelineVersionList)-1]
		isLastPPlVersionPk, err := storage.Pipeline.IsLastPipelineVersionPk(ctx.Logging(), pipelineID, pplVersion.Pk, fsFilter)
		if err != nil {
			ctx.ErrorCode = common.InternalError
			errMsg := fmt.Sprintf("get last pplversion for ppl[%s] failed. err:[%s]", pipelineID, err.Error())
			ctx.Logging().Errorf(errMsg)
			return GetPipelineResponse{}, fmt.Errorf(errMsg)
		}

		if !isLastPPlVersionPk {
			nextMarker, err := common.EncryptPk(pplVersion.Pk)
			if err != nil {
				ctx.ErrorCode = common.InternalError
				errMsg := fmt.Sprintf("EncryptPk error. pk:[%d] error:[%s]", pplVersion.Pk, err.Error())
				ctx.Logging().Errorf(errMsg)
				return GetPipelineResponse{}, fmt.Errorf(errMsg)
			}
			pipelineVersions.NextMarker = nextMarker
			pipelineVersions.IsTruncated = true
		}
	}
	pipelineVersions.MaxKeys = maxKeys
	pipelineVersions.PipelineVersionList = []PipelineVersionBrief{}
	for _, pplVersion := range pipelineVersionList {
		pipelineVersionBrief := PipelineVersionBrief{}
		pipelineVersionBrief.updateFromPipelineVersionModel(pplVersion)
		pipelineVersions.PipelineVersionList = append(pipelineVersions.PipelineVersionList, pipelineVersionBrief)
	}

	getPipelineResponse.PipelineVersions = pipelineVersions
	return getPipelineResponse, nil
}

func GetPipelineVersion(ctx *logger.RequestContext, pipelineID string, pipelineVersionID string) (GetPipelineVersionResponse, error) {
	ctx.Logging().Debugf("begin get pipeline version.")

	// query pipeline
	hasAuth, ppl, pplVersion, err := CheckPipelineVersionPermission(ctx.UserName, pipelineID, pipelineVersionID)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("get pipeline[%s] version[%s] failed. err:%v", pipelineID, pipelineVersionID, err)
		ctx.Logging().Errorf(errMsg)
		return GetPipelineVersionResponse{}, fmt.Errorf(errMsg)
	} else if !hasAuth {
		ctx.ErrorCode = common.AccessDenied
		errMsg := fmt.Sprintf("get pipeline[%s] version[%s] failed. Access denied for user[%s]", pipelineID, pipelineVersionID, ctx.UserName)
		ctx.Logging().Errorf(errMsg)
		return GetPipelineVersionResponse{}, fmt.Errorf(errMsg)
	}

	getPipelineVersionResponse := GetPipelineVersionResponse{}
	getPipelineVersionResponse.Pipeline.updateFromPipelineModel(ppl)
	getPipelineVersionResponse.PipelineVersion.updateFromPipelineVersionModel(pplVersion)
	return getPipelineVersionResponse, nil
}

func DeletePipeline(ctx *logger.RequestContext, pipelineID string) error {
	ctx.Logging().Debugf("begin delete pipeline: %s", pipelineID)

	hasAuth, _, err := CheckPipelinePermission(ctx.UserName, pipelineID)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("delete pipeline[%s] failed. err:%v", pipelineID, err)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	} else if !hasAuth {
		ctx.ErrorCode = common.AccessDenied
		errMsg := fmt.Sprintf("delete pipeline[%s] failed. Access denied for user[%s]", pipelineID, ctx.UserName)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	// 需要判断是否有周期调度运行中（单次任务不影响，因为run会直接保存yaml）
	scheduleList, err := models.ListSchedule(ctx.Logging(), 0, 0, []string{pipelineID}, []string{}, []string{}, []string{}, []string{}, models.ScheduleNotFinalStatusList)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("models list schedule failed. err:[%s]", err.Error())
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	} else if len(scheduleList) > 0 {
		ctx.ErrorCode = common.ActionNotAllowed
		errMsg := fmt.Sprintf("delete pipeline[%s] failed, there are running schedules, pls stop first", pipelineID)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	if err := storage.Pipeline.DeletePipeline(ctx.Logging(), pipelineID); err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("models delete pipeline[%s] failed. error:%s", pipelineID, err.Error())
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}
	return nil
}

func DeletePipelineVersion(ctx *logger.RequestContext, pipelineID string, pipelineVersionID string) error {
	ctx.Logging().Debugf("begin delete pipeline version[%s], with pipelineID[%s]", pipelineVersionID, pipelineID)
	hasAuth, _, _, err := CheckPipelineVersionPermission(ctx.UserName, pipelineID, pipelineVersionID)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed. err:%v", pipelineID, pipelineVersionID, err)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	} else if !hasAuth {
		ctx.ErrorCode = common.AccessDenied
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed. Access denied for user[%s]", pipelineID, pipelineVersionID, ctx.UserName)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	// 如果只有一个pipeline version的话，直接删除pipeline本身
	count, err := storage.Pipeline.CountPipelineVersion(pipelineID)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed. err:%v", pipelineID, pipelineVersionID, err)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	} else if count == 1 {
		ctx.ErrorCode = common.ActionNotAllowed
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed. only one pipeline version left, pls delete pipeline instead", pipelineID, pipelineVersionID)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	// 需要判断是否有周期调度运行中（单次任务不影响，因为run会直接保存yaml）
	scheduleList, err := models.ListSchedule(ctx.Logging(), 0, 0, []string{pipelineID}, []string{pipelineVersionID}, []string{}, []string{}, []string{}, models.ScheduleNotFinalStatusList)
	if err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("models list schedule for pipeline[%s] version[%s] failed. err:[%s]", pipelineID, pipelineVersionID, err.Error())
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	} else if len(scheduleList) > 0 {
		ctx.ErrorCode = common.ActionNotAllowed
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed, there are running schedules, pls stop first", pipelineVersionID, pipelineID)
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	if err := storage.Pipeline.DeletePipelineVersion(ctx.Logging(), pipelineID, pipelineVersionID); err != nil {
		ctx.ErrorCode = common.InternalError
		errMsg := fmt.Sprintf("delete pipeline[%s] version[%s] failed. error:%s", pipelineID, pipelineVersionID, err.Error())
		ctx.Logging().Errorf(errMsg)
		return fmt.Errorf(errMsg)
	}

	return nil
}

func CheckPipelinePermission(userName string, pipelineID string) (bool, model.Pipeline, error) {
	ppl, err := storage.Pipeline.GetPipelineByID(pipelineID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			errMsg := fmt.Sprintf("pipeline[%s] not exist", pipelineID)
			return false, model.Pipeline{}, fmt.Errorf(errMsg)
		} else {
			errMsg := fmt.Sprintf("get pipeline[%s] failed, err:[%s]", pipelineID, err.Error())
			return false, model.Pipeline{}, fmt.Errorf(errMsg)
		}
	}

	if !common.IsRootUser(userName) && userName != ppl.UserName {
		return false, model.Pipeline{}, nil
	}

	return true, ppl, nil
}

func CheckPipelineVersionPermission(userName string, pipelineID string, pipelineVersionID string) (bool, model.Pipeline, model.PipelineVersion, error) {
	hasAuth, ppl, err := CheckPipelinePermission(userName, pipelineID)
	if err != nil {
		return false, model.Pipeline{}, model.PipelineVersion{}, err
	} else if !hasAuth {
		return false, model.Pipeline{}, model.PipelineVersion{}, nil
	}

	pipelineVersion, err := storage.Pipeline.GetPipelineVersion(pipelineID, pipelineVersionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			errMsg := fmt.Sprintf("pipeline[%s] version[%s] not exist", pipelineID, pipelineVersionID)
			return false, model.Pipeline{}, model.PipelineVersion{}, fmt.Errorf(errMsg)
		} else {
			errMsg := fmt.Sprintf("get pipeline[%s] version[%s] failed, err:[%s]", pipelineID, pipelineVersionID, err.Error())
			return false, model.Pipeline{}, model.PipelineVersion{}, fmt.Errorf(errMsg)
		}
	}

	return true, ppl, pipelineVersion, nil
}
