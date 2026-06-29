export interface ChannelStatusHistoryRecord {
  id: number
  channel_id: number
  channel_name: string
  channel_type: number
  base_url: string
  group: string
  from_status: number
  to_status: number
  status_reason: string
  status_code: number
  error_code: string
  model_name: string
  trigger_source: string
  response_time_ms: number
  multi_key_index: number
  seconds_in_prev_status: number
  created_at: number
}

export interface ChannelFlapStatRow {
  channel_id: number
  channel_name: string
  base_url: string
  transitions: number
  disable_count: number
  enable_count: number
  seconds_up: number
  seconds_down: number
  uptime_percent: number
  last_to_status: number
}

export interface GetChannelStatusHistoryParams {
  p?: number
  page_size?: number
  channel_id?: number
  to_status?: number
  trigger_source?: string
  status_code?: number
  model_name?: string
  start_timestamp?: number
  end_timestamp?: number
}

export interface ChannelStatusHistoryPage {
  items: ChannelStatusHistoryRecord[]
  total: number
  page: number
  page_size: number
}
