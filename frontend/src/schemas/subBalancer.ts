import { z } from 'zod';

export const SubBalancerStrategySchema = z.enum(['leastLoad', 'leastPing', 'random', 'roundRobin']);
export type SubBalancerStrategy = z.infer<typeof SubBalancerStrategySchema>;

// The API sends null (or leaves the key out) for a list that a balancer never had,
// such as the nodes of one made before they could be picked; a list always reads as an array.
const IdListSchema = z
  .array(z.number())
  .nullish()
  .transform((ids) => ids ?? []);

export const SubBalancerSchema = z.object({
  id: z.number(),
  remark: z.string(),
  strategy: SubBalancerStrategySchema,
  inboundIds: IdListSchema,
  nodeIds: IdListSchema,
  memberWeights: z.record(z.string(), z.number()).nullish(),
  sortOrder: z.number(),
  enabled: z.boolean(),
  createdAt: z.number().optional(),
  updatedAt: z.number().optional(),
});
export type SubBalancer = z.infer<typeof SubBalancerSchema>;

export const SubBalancerListSchema = z.array(SubBalancerSchema);

export const SubBalancerFormSchema = z
  .object({
    remark: z
      .string()
      .trim()
      .min(1, 'pages.settings.subBalancers.errRemarkRequired')
      .max(256, 'pages.settings.subBalancers.errRemarkRequired'),
    strategy: SubBalancerStrategySchema,
    inboundIds: z.array(z.number().int().positive()),
    // A node stands for the inbounds the subscriber has on it.
    nodeIds: z.array(z.number().int().positive()),
    // inboundId (stringified) -> leastLoad weight; absent members weigh 1.0.
    memberWeights: z
      .record(
        z.string(),
        z
          .number({ error: 'pages.settings.subBalancers.errWeightPositive' })
          .positive('pages.settings.subBalancers.errWeightPositive'),
      )
      .optional(),
    sortOrder: z
      .number({ error: 'pages.settings.subBalancers.errSortOrder' })
      .int('pages.settings.subBalancers.errSortOrder')
      .min(1, 'pages.settings.subBalancers.errSortOrder'),
    enabled: z.boolean(),
  })
  .refine((values) => values.inboundIds.length > 0 || values.nodeIds.length > 0, {
    message: 'pages.settings.subBalancers.errMembersRequired',
    path: ['inboundIds'],
  });
export type SubBalancerFormValues = z.infer<typeof SubBalancerFormSchema>;
