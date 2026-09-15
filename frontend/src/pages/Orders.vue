<template>
  <div class="page">
    <h2>我的交易</h2>
    <el-card v-for="o in orders" :key="o.id" class="order-card">
      <div class="order-row">
        <div>
          <TradeStatusBadge :status="o.status" />
          <span class="order-id">订单 #{{ o.id }} · 商品 #{{ o.product_id }}</span>
          <p class="order-meta">
            买家 #{{ o.buyer_id }} / 卖家 #{{ o.seller_id }} · {{ formatDateTime(o.created_at) }}
          </p>
        </div>
        <div class="order-actions">
          <el-button v-if="canPropose(o)" size="small" type="primary" @click="openAppointmentDialog(o)">发起面交预约</el-button>
          <el-button v-if="o.status === 'pending' && o.buyer_id === authStore.user?.id" size="small" type="primary" plain @click="buyerConfirmFn(o.id)">确认收货</el-button>
          <el-button v-if="o.status === 'confirmed' && o.seller_id === authStore.user?.id" size="small" type="success" plain @click="sellerConfirmFn(o.id)">确认收款</el-button>
          <el-button v-if="o.status === 'pending'" size="small" type="danger" @click="cancelFn(o.id)">取消</el-button>
          <el-button v-if="o.status === 'completed'" size="small" @click="reviewDialog(o)">评价</el-button>
        </div>
      </div>
      <div v-if="o.appointment" class="appointment-block">
        <div class="appointment-info">
          <el-tag :type="appointmentStatusType(o.appointment.status) as any" size="small">{{ appointmentStatusLabel(o.appointment.status) }}</el-tag>
          <span class="appointment-line">
            面交时间：{{ formatDateTime(o.appointment.meet_at) }} · 地点：{{ o.appointment.location }}
          </span>
          <span class="appointment-confirms">
            <el-tag :type="o.appointment.buyer_confirmed_at ? 'success' : 'info'" size="small" effect="plain">
              买家{{ o.appointment.buyer_confirmed_at ? '已确认交接' : '未确认' }}
            </el-tag>
            <el-tag :type="o.appointment.seller_confirmed_at ? 'success' : 'info'" size="small" effect="plain">
              卖家{{ o.appointment.seller_confirmed_at ? '已确认交接' : '未确认' }}
            </el-tag>
          </span>
        </div>
        <div class="appointment-actions">
          <template v-if="o.appointment.status === 'pending' && o.appointment.counterpart_id === authStore.user?.id">
            <el-button size="small" type="success" @click="acceptFn(o.appointment.id)">接受</el-button>
            <el-button size="small" type="warning" @click="openRescheduleDialog(o.appointment)">改约</el-button>
            <el-button size="small" type="danger" @click="rejectFn(o.appointment.id)">拒绝</el-button>
          </template>
          <span v-else-if="o.appointment.status === 'pending'" class="appointment-hint">等待对方响应</span>
          <el-button
            v-if="o.appointment.status === 'accepted' && !myHandoverDone(o.appointment)"
            size="small" type="primary"
            @click="handoverFn(o.appointment.id)"
          >确认交接</el-button>
        </div>
      </div>
    </el-card>
    <el-empty v-if="orders.length === 0" description="暂无交易" />

    <el-dialog v-model="appointmentVisible" :title="appointmentMode === 'create' ? '发起面交预约' : '改约面交时间'" width="420px">
      <el-form label-width="80px">
        <el-form-item label="面交时间">
          <el-date-picker
            v-model="appointmentForm.meet_at"
            type="datetime"
            placeholder="选择未来的时间"
            value-format="YYYY-MM-DD HH:mm"
            :disabled-date="(d: Date) => d.getTime() < Date.now() - 86400000"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="面交地点">
          <el-input v-model="appointmentForm.location" placeholder="校内地点，如：图书馆门口" maxlength="128" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="appointmentVisible = false">取消</el-button>
        <el-button type="primary" @click="submitAppointment">提交</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="reviewVisible" title="信誉评价" width="420px">
      <el-form label-width="70px">
        <el-form-item label="评价">
          <el-select v-model="reviewForm.rating" style="width: 100%">
            <el-option v-for="r in REVIEW_RATINGS" :key="r.value" :label="r.label" :value="r.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="内容">
          <el-input v-model="reviewForm.content" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="reviewVisible = false">取消</el-button>
        <el-button type="primary" @click="submitReview">提交</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { ElMessage } from 'element-plus'
import TradeStatusBadge from '../components/common/TradeStatusBadge.vue'
import { useTradeStore } from '../stores/tradeStore'
import { useAuthStore } from '../stores/authStore'
import { buyerConfirm, sellerConfirm, cancelTradeOrder } from '../api/tradeOrder'
import {
  createAppointment, acceptAppointment, rejectAppointment,
  rescheduleAppointment, confirmHandover,
} from '../api/appointment'
import { createReview } from '../api/review'
import { REVIEW_RATINGS, appointmentStatusLabel, appointmentStatusType } from '../constants/trade'
import { formatDateTime } from '../utils/dateFormat'
import type { MeetupAppointment, TradeOrder } from '../types'

const tradeStore = useTradeStore()
const { orders } = storeToRefs(tradeStore)
const { fetch } = tradeStore
const authStore = useAuthStore()
const reviewVisible = ref(false)
const reviewForm = reactive({ trade_id: 0, rating: 'good', content: '' })

const appointmentVisible = ref(false)
const appointmentMode = ref<'create' | 'reschedule'>('create')
const appointmentForm = reactive({ order_id: 0, appointment_id: 0, meet_at: '', location: '' })

function isActiveAppointment(a: MeetupAppointment | null): boolean {
  return !!a && (a.status === 'pending' || a.status === 'accepted')
}

function canPropose(o: TradeOrder): boolean {
  const orderActive = o.status === 'pending' || o.status === 'confirmed'
  return orderActive && !isActiveAppointment(o.appointment)
}

function myHandoverDone(a: MeetupAppointment): boolean {
  const uid = authStore.user?.id
  if (uid === undefined) return true
  return mySideConfirmed(a, uid)
}

function mySideConfirmed(a: MeetupAppointment, uid: number): boolean {
  const o = orders.value.find((x) => x.id === a.order_id)
  if (!o) return true
  if (o.buyer_id === uid) return a.buyer_confirmed_at !== null
  if (o.seller_id === uid) return a.seller_confirmed_at !== null
  return true
}

function openAppointmentDialog(o: TradeOrder) {
  appointmentMode.value = 'create'
  appointmentForm.order_id = o.id
  appointmentForm.appointment_id = 0
  appointmentForm.meet_at = ''
  appointmentForm.location = ''
  appointmentVisible.value = true
}

function openRescheduleDialog(a: MeetupAppointment) {
  appointmentMode.value = 'reschedule'
  appointmentForm.order_id = a.order_id
  appointmentForm.appointment_id = a.id
  appointmentForm.meet_at = ''
  appointmentForm.location = a.location
  appointmentVisible.value = true
}

async function submitAppointment() {
  if (!appointmentForm.meet_at) {
    ElMessage.warning('请选择面交时间')
    return
  }
  if (!appointmentForm.location.trim()) {
    ElMessage.warning('请填写校内面交地点')
    return
  }
  const payload = { meet_at: appointmentForm.meet_at, location: appointmentForm.location.trim() }
  if (appointmentMode.value === 'create') {
    await createAppointment(appointmentForm.order_id, payload)
    ElMessage.success('预约已发起，等待对方响应')
  } else {
    await rescheduleAppointment(appointmentForm.appointment_id, payload)
    ElMessage.success('已发起改约，等待对方响应')
  }
  appointmentVisible.value = false
  await fetch()
}

async function acceptFn(id: number) {
  await acceptAppointment(id)
  ElMessage.success('已接受预约')
  await fetch()
}

async function rejectFn(id: number) {
  await rejectAppointment(id)
  ElMessage.success('已拒绝预约')
  await fetch()
}

async function handoverFn(id: number) {
  await confirmHandover(id)
  ElMessage.success('已确认交接')
  await fetch()
}

async function buyerConfirmFn(id: number) {
  await buyerConfirm(id)
  ElMessage.success('已确认收货')
  await fetch()
}

async function sellerConfirmFn(id: number) {
  await sellerConfirm(id)
  ElMessage.success('交易完成')
  await fetch()
}

async function cancelFn(id: number) {
  await cancelTradeOrder(id)
  ElMessage.success('已取消')
  await fetch()
}

function reviewDialog(o: TradeOrder) {
  reviewForm.trade_id = o.id
  reviewForm.rating = 'good'
  reviewForm.content = ''
  reviewVisible.value = true
}

async function submitReview() {
  await createReview({ trade_id: reviewForm.trade_id, rating: reviewForm.rating, content: reviewForm.content })
  ElMessage.success('评价成功')
  reviewVisible.value = false
}

onMounted(fetch)
</script>

<style scoped>
.order-card {
  margin-bottom: 12px;
}
.order-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.order-id {
  margin-left: 8px;
  font-size: 13px;
  color: #606266;
}
.order-meta {
  color: #909399;
  font-size: 12px;
  margin: 6px 0 0;
}
.appointment-block {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 10px;
  padding: 10px 12px;
  background: #f5f7fa;
  border-radius: 6px;
}
.appointment-info {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.appointment-line {
  font-size: 13px;
  color: #303133;
}
.appointment-confirms {
  display: inline-flex;
  gap: 6px;
}
.appointment-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.appointment-hint {
  font-size: 12px;
  color: #909399;
}
</style>
