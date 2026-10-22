import type { components } from './schema';

type S = components['schemas'];

export type Game = S['Game'];
export type PortSpec = S['PortSpec'];
export type Instance = S['Instance'];
export type InstanceState = S['InstanceState'];
export type InstanceCreate = S['InstanceCreate'];
export type PortBinding = S['PortBinding'];
export type Job = S['Job'];
export type Session = S['Session'];
export type ApiError = S['Error'];
export type InstanceAction = 'install' | 'update' | 'start' | 'stop' | 'restart';
export type ModRef = S['ModRef'];
export type ModPackage = S['ModPackage'];
export type InstalledMod = S['InstalledMod'];
export type NexusAccount = S['NexusAccount'];
export type ModProvider = InstalledMod['provider'];
