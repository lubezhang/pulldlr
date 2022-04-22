package pulldlr

import (
	"errors"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/imdario/mergo"
	"github.com/lubezhang/hls-parse/protocol"
	"github.com/lubezhang/hls-parse/types"
	"github.com/lubezhang/pulldlr/utils"
)

const CONST_BASE_SLICE_FILE_EXT = ".ts" // 分片文件扩展名

func New(url string) (result *Downloader, err error) {
	result = &Downloader{
		m3u8Url: url,
	}
	return result, nil
}

// 下载器参数
type DownloaderOption struct {
	FileName string // 文件名
}

// 下载器
type Downloader struct {
	m3u8Url    string            // m3u8文件地址
	hlsBase    *protocol.HlsBase // 协议基础对象
	selectVod  *protocol.HlsVod  // 选择下载的视频
	wg         sync.WaitGroup    // 并发线程管理容器
	opts       DownloaderOption  // 下载器参数
	cache      DownloadCacheData // 下载数据管理器
	sliceCount int               // 下载进度，完成文件合并的分片数量
}

// 设置参数
func (dl *Downloader) SetOpts(opts1 DownloaderOption) {
	dl.opts = opts1
}

// 开始下载m3u8文件
func (dl *Downloader) Start() {
	// _, err := dl.selectMediaVod()
	dl._init()
	if reflect.ValueOf(dl.selectVod).IsValid() {
		go dl.mergeVodFileToMp4()
		dl.startDownload()
		dl.wg.Wait()
		dl.cleanTmpFile() // 下载完成，清理临时数据
	} else {
		utils.LoggerInfo("没有选择下载的视频")
		return
	}
	utils.LoggerInfo("<<<<<<< 下载视频完成:" + dl.opts.FileName)
}

func (dl *Downloader) _init() {
	dl.sliceCount = 0
	// 设置默认参数
	defaultOpts := DownloaderOption{
		FileName: time.Now().Format("2006-01-02$15:04:05") + ".mp4", // 生成临时文件名
	}
	mergo.MergeWithOverwrite(&defaultOpts, dl.opts) // 合并自定义和默认参数
	utils.LoggerInfo(">>>>>>> 下载视频:" + defaultOpts.FileName)
	dl.SetOpts(defaultOpts)

	dl.selectMediaVod()
}

func (dl *Downloader) mergeVodFileToMp4() {
	sliceTotal := len(dl.selectVod.ExtInfs)
	if dl.sliceCount >= sliceTotal { // 所有分片文件已经完成合并
		dl.wg.Done()
		return
	}

	for {
		utils.LoggerInfo("******* 视频下载进度：" + strconv.Itoa(dl.sliceCount) + " / " + strconv.Itoa(sliceTotal))
		// 检查片文件是否存在
		sliceFilePath := dl.getTmpFilePath(strconv.Itoa(dl.sliceCount))
		_, err1 := os.Stat(sliceFilePath)
		if err1 != nil {
			break
		}
		utils.LoggerDebug("读取一个分片文件:" + sliceFilePath)

		// 读取一个分片文件
		tsFile, err2 := os.OpenFile(sliceFilePath, os.O_RDONLY, os.ModePerm)
		if err2 != nil {
			break
		}

		buf, _ := ioutil.ReadAll(tsFile)
		buf = utils.CleanSliceUselessData(buf)
		vodFile, _ := os.OpenFile(dl.getVodFilePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm)
		vodFile.Write(buf)

		tsFile.Close()
		vodFile.Close()
		dl.sliceCount = dl.sliceCount + 1
		time.Sleep(80 * time.Millisecond)
	}
	time.Sleep(1 * time.Second) // 等待一会，在继续执行合并操作
	dl.mergeVodFileToMp4()
}

func (dl *Downloader) startDownload() {
	dl.setDecryptKey()
	dl.setDwnloadCache()
	dl.startDownloadVod()
}

// 开始下载Vod文件
func (dl *Downloader) startDownloadVod() {
	for i := 0; i < 10; i++ { // 开启10个协程下载
		go dl.downloadVodFile()
		// dl.downloadVodFile()
	}
}

// 将视频片放到数据下载管理器中
func (dl *Downloader) setDwnloadCache() {
	hlsVod := dl.selectVod
	if len(hlsVod.ExtInfs) == 0 {
		return
	}

	var list []DownloadData
	for idx, extinf := range hlsVod.ExtInfs {
		var decryptKey = ""
		if extinf.EncryptIndex >= 0 {
			decryptKey = hlsVod.Extkeys[extinf.EncryptIndex].Key
		}
		list = append(list, DownloadData{
			Index:        idx,
			Key:          utils.GetMD5(extinf.Url),
			Title:        extinf.Title,
			Url:          extinf.Url,
			DownloadPath: dl.getTmpFilePath(strconv.Itoa(idx)),
			EncryptKey:   decryptKey,
		})
	}

	dl.wg.Add(len(list) + 1) // 初始化并发线程计数器
	dl.cache.Push(list)
}

func (dl *Downloader) downloadVodFile() {
	for {
		data, err := dl.cache.Pop()
		if err != nil {
			return
		}
		utils.DownloadeSliceFile(data.Url, data.DownloadPath, data.EncryptKey)
		time.Sleep(20 * time.Millisecond)
		utils.LoggerDebug("分片下载完成:" + data.DownloadPath)
		dl.wg.Done()
	}
}

// 解析vod类型的协议
func (dl *Downloader) selectMediaVod() (err error) {
	utils.LoggerInfo("获取Vod协议文件对象")
	baseUrl := utils.GetBaseUrl(dl.m3u8Url)

	data1, _ := utils.HttpGetFile(dl.m3u8Url)
	strDat1 := string(data1)
	hlsBase, _ := protocol.ParseString(&strDat1, baseUrl)

	if hlsBase.IsMaster() {
		dl.hlsBase = &hlsBase
		hlsMaster, _ := hlsBase.GetMaster()
		if len(hlsMaster.StreamInfs) == 0 {
			return errors.New("master中没有视频流")
		}
		data2, _ := utils.HttpGetFile(hlsMaster.StreamInfs[0].Url)
		strData2 := string(data2)
		hlsBas2, _ := protocol.ParseString(&strData2, baseUrl)
		if hlsBas2.IsVod() {
			selectVod, _ := hlsBase.GetVod()
			dl.selectVod = &selectVod
		}
	} else if hlsBase.IsVod() {
		selectVod, _ := hlsBase.GetVod()
		dl.selectVod = &selectVod
	} else {
		return errors.New("没有视频回放文件")
	}
	err = nil
	return
}

// 通过链接获取加密密钥，并将密钥填充到加密数据结构中
func (dl *Downloader) setDecryptKey() {
	hls := dl.selectVod
	if len(hls.Extkeys) == 0 {
		return
	}

	var keys []types.TagExtKey
	for _, extkey := range hls.Extkeys {
		if extkey.Method == "AES-128" {
			tmp := extkey
			data, _ := utils.HttpGetFile(extkey.Uri)
			tmp.Key = string(data)
			keys = append(keys, tmp)
		}
	}

	hls.Extkeys = keys
}

func (dl *Downloader) getTmpFilePath(fileName string) string {
	return path.Join(utils.GetDownloadTmpDir(), utils.GetMD5(dl.opts.FileName), fileName+CONST_BASE_SLICE_FILE_EXT)
}
func (dl *Downloader) getVodFilePath() string {
	return path.Join(utils.GetDownloadDataDir(), dl.opts.FileName)
}

// 清理临时文件
func (dl *Downloader) cleanTmpFile() error {
	utils.LoggerInfo("清理临时文件")
	tmpDir := dl.getTmpFilePath(dl.opts.FileName)
	err := os.RemoveAll(tmpDir)
	if err != nil {
		return err
	}
	return nil
}
