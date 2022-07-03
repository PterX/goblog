import React, { useRef } from 'react';
import { PageContainer } from '@ant-design/pro-layout';
import type { ProColumns, ActionType } from '@ant-design/pro-table';
import ProTable from '@ant-design/pro-table';
import moment from 'moment';
import { getStatisticInfo } from '@/services/statistic';
import { useModel } from 'umi';

const StatisticDetail: React.FC = () => {
  const actionRef = useRef<ActionType>();
  const { initialState } = useModel('@@initialState');

  const openLink = (text: string) => {
    window.open((initialState?.system?.base_url || '') + text)
  }

  const columns: ProColumns<any>[] = [
    {
      title: '时间',
      dataIndex: 'created_time',
      render: (text, record) => moment(record.created_time * 1000).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '域名',
      dataIndex: 'host',
    },
    {
      title: '访问地址',
      dataIndex: 'url',
      width: 200,
      ellipsis: true,
      render: (text, record) => <div className='link' onClick={() => openLink(record.url)}>{text}</div>,
    },
    {
      title: 'IP',
      dataIndex: 'ip',
    },
    {
      title: '设备',
      dataIndex: 'device',
      width: 100,
    },
    {
      title: '蜘蛛',
      dataIndex: 'spider',
      width: 80,
    },
    {
      title: '状态码',
      dataIndex: 'http_code',
      width: 80,
    },
    {
      title: '请求UA',
      dataIndex: 'user_agent',
      width: 300,
      ellipsis: true,
    },
  ];

  return (
    <PageContainer>
      <ProTable<any>
        headerTitle="浏览详细记录"
        actionRef={actionRef}
        rowKey="id"
        search={false}
        request={(params, sort) => {
          return getStatisticInfo(params);
        }}
        columns={columns}
      />
    </PageContainer>
  );
};

export default StatisticDetail;
