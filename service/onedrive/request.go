package onedrive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"gonelist/conf"
	"gonelist/service/onedrive/model"
)

var (
	// 默认 https://graph.microsoft.com/v1.0/me/drive/root/children
	// ChinaCloud https://microsoftgraph.chinacloudapi.cn/v1.0/me/drive/root/children
	ROOTUrl  string
	UrlBegin string
	UrlEnd   string
)

func SetROOTUrl(conf *conf.AllSet) {
	user := conf.Onedrive
	ROOTUrl = user.RemoteConf.ROOTUrl
	UrlBegin = user.RemoteConf.UrlBegin
	UrlEnd = user.RemoteConf.UrlEnd
}

func Upload(path string, fileName string, content []byte) error {
	baseURL := "https://graph.microsoft.com/v1.0/me/drive/root:" + path + "/" + url.PathEscape(fileName) + ":/content"
	log.Infoln(baseURL)
	resp, err := putOneURL("PUT", baseURL, map[string]string{}, content)
	if err != nil {
		return err
	}
	log.Infoln(string(resp))
	err = RefreshOnedriveAll()
	if err != nil {
		return err
	}
	return nil
}

func Delta(token string) (Answer, string, error) {
	var (
		ans     Answer
		tempAns Answer
		baseURL string
	)
	baseURL = token
	if baseURL == "" {
		baseURL = "https://graph.microsoft.com/v1.0/me/drive/root/delta"
	}
	//if token == "" {
	//	baseURL = "https://graph.microsoft.com/v1.0/me/drive/root/delta"
	//} else {
	//	baseURL = "https://graph.microsoft.com/v1.0/me/drive/root/delta?token=" + token
	//}
	for {
		resp, err := putOneURL(http.MethodGet, baseURL, map[string]string{}, nil)
		if err != nil {
			return ans, "", err
		}
		HandleDeltaResp(resp)
		err = json.Unmarshal(resp, &tempAns)
		if err != nil {
			log.Errorln(err.Error())
			return ans, "", err
		}
		if len(tempAns.Value) == 0 {
			break
		}
		if len(ans.Value) == 0 {
			ans = tempAns
		} else {
			ans.Value = append(ans.Value, tempAns.Value...)
		}
		if tempAns.OdataNextLink == "" {
			baseURL = tempAns.OdataDeltaLink
			break
		} else {
			baseURL = tempAns.OdataNextLink
		}

	}
	return ans, baseURL, nil

}

// HandleDeltaResp
/**
 * @Description: 对获取到的变更数据进行处理
 * @param data
 */
func HandleDeltaResp(data []byte) {
	values := gjson.GetBytes(data, "value").Array()
	if len(values) == 0 {
		return
	}
	for _, value := range values {
		node := ValueToNode(value.String())
		if gjson.Get(value.String(), "deleted.state").String() == "deleted" {
			if node.Name == ".password" {
				parentNode, _ := model.Find(node.ParentID)
				parentNode.Password = ""
				parentNode.PasswordURL = ""
				_ = model.UpdateFile(parentNode)
			}
			_ = model.DeleteFile(node.ID)
		} else {
			node1, err := model.Find(node.ID)
			if err != nil || node1.ID == "" {
				_ = model.InsetFile(node)
			} else {
				_ = model.UpdateFile(node)
			}
		}
	}
}

// ValueToNode
/**
 * @Description: 将返回的item转化成对应的FileNode对象
 * @param value
 * @return *model.FileNode
 */
func ValueToNode(value string) *model.FileNode {
	node := new(model.FileNode)
	node.ID = gjson.Get(value, "id").String()
	node.Name = gjson.Get(value, "name").String()
	if gjson.Get(value, "root").Exists() {
		node.Path = "/"
		node.ParentID = ""
		node.IsFolder = true
	} else {
		s := gjson.Get(value, "parentReference.path").String()
		node.Path = strings.ReplaceAll(s, "/drive/root:", "") + "/" + node.Name
		node.ParentID = gjson.Get(value, "parentReference.id").String()
	}
	if gjson.Get(value, "folder").Exists() {
		node.IsFolder = true
	}
	modifyTime, _ := time.Parse("2006-01-02T15:04:05Z", gjson.Get(value, "lastModifiedDateTime").String())
	node.LastModifyTime = modifyTime
	node.Size = gjson.Get(value, "size").Int()
	return node
}

// Mkdir 文件夹创建
func Mkdir(path, floderName string) error {
	//node, err := GetNode(path)
	//if err != nil {
	//	return err
	//}
	node, err := model.FindByPath(path)
	if err != nil {
		return err
	}
	m := map[string]interface{}{"name": floderName, "folder": map[string]string{}}
	data, _ := json.Marshal(m)
	baseURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children",
		node.ID)
	_, err = putOneURL(http.MethodPost, baseURL, map[string]string{"Content-Type": "application/json"}, data)
	if err != nil {
		return err
	}
	return err

}

func putOneURL(method, url1 string, headers map[string]string, data []byte) ([]byte, error) {
	var (
		resp *http.Response
		body []byte
		err  error
	)

	client := GetClient()
	if client == nil {
		log.Errorln("cannot get client to start request.")
		return nil, fmt.Errorf("RequestOneURL cannot get client")
	}
	request, err := http.NewRequest(method, url1, bytes.NewReader(data))
	for key, value := range headers {
		request.Header.Add(key, value)
	}
	// 如果超时，重试两次
	for retryCount := 3; retryCount > 0; retryCount-- {
		if resp, err = client.Do(request); err != nil && strings.Contains(err.Error(), "timeout") {
			log.WithFields(log.Fields{
				"url":  url1,
				"resp": resp,
				"err":  err,
			}).Info("RequestOneUrl 出现错误，开始重试")
			<-time.After(time.Second / 3)
		} else {
			break
		}
	}

	if err != nil {
		log.WithFields(log.Fields{
			"url":  url1,
			"resp": resp,
			"err":  err,
		}).Info("请求 graph.microsoft.com 失败, request timeout")
		return body, err
	}

	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		log.WithField("err", err).Info("读取 graph.microsoft.com 返回内容失败")
		return body, err
	}
	return body, nil
}

// 获取所有文件的树
func GetAllFiles() (*model.FileNode, error) {
	var (
		err  error
		root *model.FileNode
	)

	root = &model.FileNode{
		Name:           "root",
		Path:           "/",
		IsFolder:       false,
		LastModifyTime: time.Now(),
		Children:       nil,
	}

	list, readmeUrl, passUrl, err := GetTreeFileNode("/")
	if err != nil {
		log.Info(err)
		return nil, err
	}
	root.Children = list
	root.READMEUrl = readmeUrl
	root.PasswordURL = passUrl
	if root.Children != nil {
		root.IsFolder = true
	}

	// 更新树结构
	FileTree.SetRoot(root)
	return root, nil
}

// 获取树的一个节点
// list 返回当前文件夹中的所有文件夹和文件
// readmeUrl 这个是当前文件夹 readme 文件的下载链接
// err 返回错误
func GetTreeFileNode(relativePath string) (list []*model.FileNode, readmeUrl, passUrl string, err error) {
	var (
		ans Answer
	)

	ans, err = GetUrlToAns(relativePath)
	if err != nil {
		log.WithFields(log.Fields{
			"ans": ans,
			"err": err,
		}).Errorf("请求 graph.microsoft.com 出现错误: relativePath:%v", relativePath)
		return nil, "", "", err
	}

	// 解析对应 list
	list = ConvertAnsToFileNodes(relativePath, ans)
	for i := range list {
		// 如果下一层是文件夹则继续
		if list[i].IsFolder == true {
			tmpList, tmpReadmeUrl, tmpPassUrl, err := GetTreeFileNode(list[i].Path)
			if err == nil {
				list[i].Children = tmpList
				list[i].READMEUrl = tmpReadmeUrl
				list[i].PasswordURL = tmpPassUrl
			}
		} else if list[i].Name == "README.md" {
			readmeUrl = list[i].DownloadURL
		} else if list[i].Name == ".password" {
			passUrl = list[i].DownloadURL
			// 隐藏 .password 文件的 url 和 size
			list[i].DownloadURL = ""
			list[i].Size = 0
		}
	}
	return list, readmeUrl, passUrl, nil
}

// 获取某个路径的内容，如果 token 失效或没有正常结果返回 err
func GetUrlToAns(relativePath string) (Answer, error) {
	// 默认一次获取 3000 个文件
	var (
		baseURL string
		ans     Answer
		tmpAns  Answer
		err     error
	)

	// 每次获取 3000 个文件
	switch {
	case relativePath == "/" && conf.UserSet.Onedrive.FolderSub == "/":
		// https://graph.microsoft.com/v1.0/me/drive/root/children
		baseURL = ROOTUrl + "?$top=3000"
	case relativePath == "/":
		// eg. /test -> https://graph.microsoft.com/v1.0/me/drive/root:/test:/children
		// UrlBegin: https://graph.microsoft.com/v1.0/me/drive/root:
		// conf.UserSet.Server.FolderSub: /public
		// UrlEnd: :/children
		baseURL = UrlBegin + conf.UserSet.Onedrive.FolderSub + UrlEnd + "?$top=3000"
	default:
		baseURL = UrlBegin + conf.UserSet.Onedrive.FolderSub + relativePath + UrlEnd + "?$top=3000"
		baseURL = strings.ReplaceAll(baseURL, "+", "%20")
		baseURL = strings.ReplaceAll(baseURL, "%", "%25")
	}

	for {
		tmpAns, err = RequestAnswer(baseURL, relativePath)
		// 判断是否合并两次请求
		if len(ans.Value) == 0 {
			ans = tmpAns
		} else {
			ans.Value = append(ans.Value, tmpAns.Value...)
		}
		// 判断是否继续请求下一个链接
		if err != nil {
			return ans, err
		} else if tmpAns.OdataNextLink == "" {
			break
		}
		baseURL = ans.OdataNextLink
	}

	return ans, nil
}

func RequestAnswer(urlstr string, relativePath string) (Answer, error) {
	var (
		ans Answer
	)
	if strings.Contains(urlstr, "%") {
		log.Debugf("123")
	}
	// 首先对非英文字符进行 encode，兼容出现 %20 和 特殊字符 等情况
	//encodeURL := url.QueryEscape(urlstr)
	//body, err := RequestOneUrl(encodeURL)
	//m, err := url.Parse(urlstr)
	//if err != nil {
	//	log.Infof("url 出现问题，url:%s", urlstr)
	//	return ans, fmt.Errorf("url 出现问题")
	//}
	//encodeURL := m.String()
	//body, err := RequestOneUrl(encodeURL)

	//encodeURL := url.QueryEscape(urlstr)
	//body, err := RequestOneUrl(encodeURL)

	body, err := RequestOneUrl(urlstr)
	if err != nil {
		return ans, err
	}

	// 解析内容
	if err := json.Unmarshal(body, &ans); err != nil {
		return ans, err
	}
	log.Debugf("url:%s relativePath:%s | body:%s", urlstr, relativePath, string(body))
	err = CheckAnswerValid(ans, relativePath)

	//如果获取内容不正常，则返回
	if err != nil {
		return ans, err
	}
	return ans, nil
}

// 请求 onedrive 的原始 URL 数据
func RequestOneUrl(url string) (body []byte, err error) {
	var (
		client *http.Client // 获取全局的 client 来请求接口
		resp   *http.Response
	)
	if client = GetClient(); client == nil {
		log.Errorln("cannot get client to start request.")
		return nil, fmt.Errorf("RequestOneURL cannot get client")
	}

	// 如果超时，重试两次
	for retryCount := 3; retryCount > 0; retryCount-- {
		if resp, err = client.Get(url); err != nil && strings.Contains(err.Error(), "timeout") {
			log.WithFields(log.Fields{
				"url":  url,
				"resp": resp,
				"err":  err,
			}).Info("RequestOneUrl 出现错误，开始重试")
			<-time.After(time.Second / 3)
		} else {
			break
		}
	}

	if err != nil {
		log.WithFields(log.Fields{
			"url":  url,
			"resp": resp,
			"err":  err,
		}).Info("请求 graph.microsoft.com 失败, request timeout")
		return body, err
	}

	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		log.WithField("err", err).Info("读取 graph.microsoft.com 返回内容失败")
		return body, err
	}
	return body, nil
}
