import { useState } from 'react'
import type { ColumnDef, PaginationState } from '@tanstack/react-table'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'
import {
  DataTablePage,
  DataTableRow,
  useDataTable,
} from '@/components/data-table'
import { getChannelFlapStats, getChannelStatusHistory } from './api'
import { formatDuration, statusLabel, statusVariant } from './constants'
import type {
  ChannelFlapStatRow,
  ChannelStatusHistoryRecord,
} from './types'

function StatusTransition(props: { from: number; to: number }) {
  const { t } = useTranslation()
  return (
    <div className='flex items-center gap-1'>
      <StatusBadge
        label={t(statusLabel(props.from))}
        variant={statusVariant(props.from)}
        size='sm'
      />
      <span className='text-muted-foreground'>{'->'}</span>
      <StatusBadge
        label={t(statusLabel(props.to))}
        variant={statusVariant(props.to)}
        size='sm'
      />
    </div>
  )
}

function useHistoryColumns(): ColumnDef<ChannelStatusHistoryRecord>[] {
  const { t } = useTranslation()
  return [
    {
      accessorKey: 'created_at',
      header: t('Time'),
      cell: ({ row }) => (
        <span className='font-mono text-xs tabular-nums'>
          {formatTimestampToDate(row.original.created_at)}
        </span>
      ),
      size: 160,
    },
    {
      accessorKey: 'channel_id',
      header: t('Channel'),
      cell: ({ row }) => (
        <div className='flex min-w-0 flex-col'>
          <span className='font-mono text-xs'>#{row.original.channel_id}</span>
          <span className='text-muted-foreground truncate text-xs'>
            {row.original.channel_name}
          </span>
        </div>
      ),
      size: 200,
    },
    {
      id: 'transition',
      header: t('Status'),
      cell: ({ row }) => (
        <StatusTransition from={row.original.from_status} to={row.original.to_status} />
      ),
      size: 220,
    },
    {
      accessorKey: 'trigger_source',
      header: t('Source'),
      cell: ({ row }) => (
        <StatusBadge label={t(row.original.trigger_source)} variant='neutral' size='sm' />
      ),
      size: 140,
    },
    {
      accessorKey: 'status_code',
      header: t('Code'),
      cell: ({ row }) =>
        row.original.status_code ? (
          <span className='font-mono text-xs'>{row.original.status_code}</span>
        ) : (
          <span className='text-muted-foreground'>-</span>
        ),
      size: 70,
    },
    {
      accessorKey: 'model_name',
      header: t('Model'),
      cell: ({ row }) => (
        <span className='truncate text-xs'>{row.original.model_name || '-'}</span>
      ),
      size: 180,
    },
    {
      accessorKey: 'seconds_in_prev_status',
      header: t('Time in Previous Status'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {formatDuration(row.original.seconds_in_prev_status)}
        </span>
      ),
      size: 120,
    },
    {
      accessorKey: 'status_reason',
      header: t('Reason'),
      cell: ({ row }) => (
        <span className='text-muted-foreground line-clamp-2 text-xs'>
          {row.original.status_reason || '-'}
        </span>
      ),
      size: 360,
    },
  ]
}

function HistoryTab() {
  const { t } = useTranslation()
  const columns = useHistoryColumns()
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 50,
  })

  const query = useQuery({
    queryKey: [
      'channel-status-history',
      'list',
      pagination.pageIndex,
      pagination.pageSize,
    ],
    queryFn: () =>
      getChannelStatusHistory({
        p: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
      }),
  })

  const data = query.data?.data
  const items = data?.items ?? []

  const { table } = useDataTable({
    data: items as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    pagination,
    enableRowSelection: false,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
    totalCount: data?.total ?? 0,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns as ColumnDef<Record<string, unknown>>[]}
      isLoading={query.isLoading}
      isFetching={query.isFetching}
      emptyTitle={t('No Status History')}
      emptyDescription={t(
        'No channel status changes recorded yet. Transitions appear here as channels are enabled or disabled.'
      )}
      renderRow={(row) => <DataTableRow key={row.id} row={row} />}
    />
  )
}

function useStatsColumns(): ColumnDef<ChannelFlapStatRow>[] {
  const { t } = useTranslation()
  return [
    {
      accessorKey: 'channel_id',
      header: t('Channel'),
      cell: ({ row }) => (
        <div className='flex min-w-0 flex-col'>
          <span className='font-mono text-xs'>#{row.original.channel_id}</span>
          <span className='text-muted-foreground truncate text-xs'>
            {row.original.channel_name}
          </span>
        </div>
      ),
      size: 220,
    },
    {
      accessorKey: 'transitions',
      header: t('Transitions'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>{row.original.transitions}</span>
      ),
      size: 110,
    },
    {
      accessorKey: 'disable_count',
      header: t('Disables'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>{row.original.disable_count}</span>
      ),
      size: 100,
    },
    {
      accessorKey: 'uptime_percent',
      header: t('Uptime'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {row.original.uptime_percent.toFixed(2)}%
        </span>
      ),
      size: 100,
    },
    {
      accessorKey: 'seconds_down',
      header: t('Total Downtime'),
      cell: ({ row }) => (
        <span className='font-mono text-xs'>
          {formatDuration(row.original.seconds_down)}
        </span>
      ),
      size: 140,
    },
    {
      accessorKey: 'last_to_status',
      header: t('Current'),
      cell: ({ row }) => (
        <StatusBadge
          label={t(statusLabel(row.original.last_to_status))}
          variant={statusVariant(row.original.last_to_status)}
          size='sm'
        />
      ),
      size: 150,
    },
  ]
}

function StatsTab() {
  const { t } = useTranslation()
  const columns = useStatsColumns()
  const [orderBy, setOrderBy] = useState('transitions')
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 100,
  })

  const query = useQuery({
    queryKey: ['channel-status-history', 'stats', orderBy],
    queryFn: () => getChannelFlapStats({ order_by: orderBy, limit: 200 }),
  })

  const rows = query.data?.data ?? []

  const { table } = useDataTable({
    data: rows as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    pagination,
    enableRowSelection: false,
    onPaginationChange: setPagination,
    manualPagination: false,
    manualFiltering: true,
    totalCount: rows.length,
  })

  return (
    <div className='flex h-full flex-col gap-3'>
      <Tabs value={orderBy} onValueChange={setOrderBy}>
        <TabsList>
          <TabsTrigger value='transitions'>{t('Most Flapping')}</TabsTrigger>
          <TabsTrigger value='uptime'>{t('Worst Uptime')}</TabsTrigger>
          <TabsTrigger value='downtime'>{t('Most Downtime')}</TabsTrigger>
        </TabsList>
      </Tabs>
      <div className='min-h-0 flex-1'>
        <DataTablePage
          table={table}
          columns={columns as ColumnDef<Record<string, unknown>>[]}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          emptyTitle={t('No Status History')}
          emptyDescription={t('No channel status changes recorded yet.')}
          renderRow={(row) => <DataTableRow key={row.id} row={row} />}
        />
      </div>
    </div>
  )
}

export function ChannelStatusHistory() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>
        {t('Channel Status History')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Tabs defaultValue='history' className='flex h-full flex-col gap-3'>
          <TabsList>
            <TabsTrigger value='history'>{t('History')}</TabsTrigger>
            <TabsTrigger value='stats'>{t('Channel Analytics')}</TabsTrigger>
          </TabsList>
          <TabsContent value='history' className='min-h-0 flex-1'>
            <HistoryTab />
          </TabsContent>
          <TabsContent value='stats' className='min-h-0 flex-1'>
            <StatsTab />
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
