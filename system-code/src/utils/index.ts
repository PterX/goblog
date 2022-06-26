import config from "@/services/config";
import { message } from "antd";
import { getStore } from "./store";

/**
 * 校验是否登录
 * @param permits
 */
export const checkLogin = (permits: any): boolean => !!permits;

export const queryParams = (params: any) => {
  let _result = [];
  for (let key in params) {
    let value = params[key];
    if (value && value.constructor === Array) {
      value.forEach(function (_value) {
        _result.push(key + '=' + _value);
      });
    } else {
      _result.push(key + '=' + value);
    }
  }
  return _result.join('&');
};

export const showNumber = (num: number) => {
  let result: any = '';
  if (num > 100000000) {
    result = (num / 100000000).toFixed(2) + '亿';
  } else if (num > 100000) {
    result = (num / 100000).toFixed(2) + '万';
  } else {
    result = num;
  }

  return result;
};

export const sizeFormat = (num: number) => {
  let result: any = '';
  if (num > 1000000) {
    result = (num / 1048576).toFixed(2) + 'MB';
  } else if (num > 500) {
    result = (num / 1024).toFixed(2) + 'KB';
  } else {
    result = num + "B";
  }

  return result;
};

// 只支持csv，excel
export const exportFile = (titles: string[], data: any[][], type?: string) => {
  type = type || 'csv';

  var textType = {
      csv: 'text/csv',
      xls: 'application/vnd.ms-excel',
    }[type],
    alink = document.createElement('a');

  alink.href =
    'data:' +
    textType +
    ';charset=utf-8,\ufeff' +
    encodeURIComponent(
      (function () {
        let content = '';
        if (type == 'csv') {
          content = titles.join(',') + '\r\n' + data.join('\r\n');
        } else {
          content += '<table border=1><thead><tr>';
          //表头
          for (let item of titles) {
            content += '<th>' + item + '</th>';
          }
          content += '</tr></thead>';
          //表体
          content += '<tbody>';
          for (let item of data) {
            content += '<tr>';
            for (let val of item) {
              content += '<td>' + val + '</td>';
            }
            content += '</tr>';
          }
          content += '</tbody>';
          content += '<table>';
        }

        return content;
      })(),
    );

  alink.download = 'table_' + new Date().getTime() + '.' + type;
  document.body.appendChild(alink);
  alink.click();
  document.body.removeChild(alink);
};

export const removeHtmlTag = (str: string) => {
  str = str
    .replace(/<style[\S\s]+?<\/style>/g, '')
    .replace(/<script[\S\s]+?<\/script>/g, '')
    .replace(/<\/[\S\s]+?>/g, '\n')
    .replace(/<[\S\s]+?>/g, '');
  str = str
    .replaceAll(' ', ' ')
    .replaceAll('&nbsp;', ' ')
    .replaceAll('&ensp;', ' ')
    .replaceAll('&emsp;', ' ');
  str = str
    .replace(/(\n *){2,}/g, '\n')
    .replace(/[ ]{2,}/g, ' ')
    .trim();

  return str;
};

export const getWordsCount = function (str: string) {
  var n = 0;
  for (var i = 0; i < str.length; i++) {
    var ch = str.charCodeAt(i);
    if (ch > 255) {
      // 中文字符集
      n += 2;
    } else {
      n++;
    }
  }
  return n;
};

export const case2Camel = function (str: string) {
  return str.replaceAll('_', ' ').toLowerCase().replace(/( |^)[a-z]/g, (L) => L.toUpperCase()).replaceAll(' ', '');
}

export const downloadFile = (url: string, params?: any, fileName?: string) => {
  //强制等待1秒钟
  let hide = message.loading('正在下载中');

  let headers = {
    admin: getStore('adminToken'),
    'Content-Type': 'application/json',
  };
  if (!fileName) {
    fileName = 'file';
  }
  return fetch(config.baseUrl + url, {
    headers: headers,
    method: 'post',
    body: JSON.stringify(params),
  })
    .then((res: any) => {
      fileName =
        res.headers
          .get('Content-Disposition')
          ?.split(';')[1]
          ?.split('filename=')[1]
          .replace(/"/g, '') || fileName;
      res
        .blob()
        .then((blob: any) => {
          if (blob.type == 'application/json') {
            //json 报错了
            var reader = new FileReader();
            reader.readAsText(blob, 'utf-8');
            reader.onload = () => {
              let data = JSON.parse(reader.result as string);
              message.error(data.msg);
            };
            return;
          }
          let a = document.createElement('a');
          let blobUrl = window?.URL?.createObjectURL(blob);
          a.href = blobUrl;
          a.download = fileName + '';
          a.click();
          window?.URL?.revokeObjectURL(blobUrl);
        })
        .catch((err: any) => {
          console.log(err);
          message.destroy();
          message.error('文件打包失败' + err);
        });
    })
    .finally(() => {
      hide();
      return Promise.resolve({});
    });
};

